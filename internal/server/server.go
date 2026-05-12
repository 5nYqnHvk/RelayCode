// Package server implements the Anthropic-compatible HTTP ingress.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/optim"
	"github.com/5nYqnHvk/RelayCode/internal/provider"
	anthropicprovider "github.com/5nYqnHvk/RelayCode/internal/provider/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/provider/chat"
	"github.com/5nYqnHvk/RelayCode/internal/provider/responses"
	"github.com/5nYqnHvk/RelayCode/internal/router"
	"github.com/5nYqnHvk/RelayCode/internal/session"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
	"github.com/5nYqnHvk/RelayCode/internal/webtools"
)

const maxRequestBodyBytes = 32 << 20

type Server struct {
	cfg      *config.Config
	router   *router.Router
	adapters map[string]provider.Adapter // lazy, keyed by provider name in cfg
	mu       sync.Mutex
	sessions *session.Store
	addr     string
}

func New(cfg *config.Config) (*Server, error) {
	for name, pc := range cfg.Providers {
		if pc.Kind != config.KindOpenAIChat && pc.Kind != config.KindOpenAIResponses && pc.Kind != config.KindAnthropicMessages {
			return nil, fmt.Errorf("provider %q: unsupported kind %q", name, pc.Kind)
		}
	}
	return &Server{
		cfg:      cfg,
		router:   router.New(cfg),
		adapters: map[string]provider.Adapter{},
		sessions: session.NewStore(60*time.Minute, 1000),
		addr:     net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)),
	}, nil
}

func (s *Server) adapterFor(name string, pc config.ProviderConfig) (provider.Adapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if a, ok := s.adapters[name]; ok {
		return a, nil
	}
	var (
		a   provider.Adapter
		err error
	)
	switch pc.Kind {
	case config.KindOpenAIChat:
		a, err = chat.New(pc)
	case config.KindOpenAIResponses:
		a, err = responses.New(pc)
	case config.KindAnthropicMessages:
		a, err = anthropicprovider.New(pc)
	default:
		return nil, fmt.Errorf("provider %q: unsupported kind %q", name, pc.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	if aware, ok := a.(provider.SessionAware); ok {
		aware.SetSession(s.sessions, name)
	}
	s.adapters[name] = a
	return a, nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/debug/stats", s.handleStats)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.Server.AuthToken != "" && !authOK(r, s.cfg.Server.AuthToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	snapshot := s.sessions.Snapshot()
	type entryView struct {
		Provider      string `json:"provider"`
		UpstreamModel string `json:"upstream_model"`
		MessageCount  int    `json:"message_count"`
		ResponseID    string `json:"response_id"`
		LastUsed      string `json:"last_used"`
		InputTokens   int    `json:"input_tokens"`
		OutputTokens  int    `json:"output_tokens"`
	}
	entries := make([]entryView, 0, len(snapshot))
	for _, e := range snapshot {
		entries = append(entries, entryView{
			Provider:      e.Provider,
			UpstreamModel: e.UpstreamModel,
			MessageCount:  e.MessageCount,
			ResponseID:    e.ResponseID,
			LastUsed:      e.LastUsed.UTC().Format(time.RFC3339),
			InputTokens:   e.InputTokens,
			OutputTokens:  e.OutputTokens,
		})
	}
	out := map[string]any{
		"sessions": entries,
		"counters": map[string]int64{
			"hits":            s.sessions.Stats.Hits.Load(),
			"misses":          s.sessions.Stats.Misses.Load(),
			"forced_replays":  s.sessions.Stats.ForcedReplays.Load(),
			"expired_invalid": s.sessions.Stats.ExpiredInvalid.Load(),
			"input_tokens":    s.sessions.Stats.InputTokens.Load(),
			"output_tokens":   s.sessions.Stats.OutputTokens.Load(),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.cfg.Server.AuthToken != "" {
		if !authOK(r, s.cfg.Server.AuthToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var req anthropic.Request
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), status)
		return
	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if os.Getenv("RELAYCODE_DEBUG_REQUEST") == "1" {
		log.Printf("incoming raw body (%d bytes): %s", len(rawBody), string(rawBody))
	}
	if s.cfg.Server.LogRequestSnapshots || os.Getenv("RELAYCODE_LOG_REQUEST_SNAPSHOTS") == "1" {
		log.Printf("incoming request snapshot: %s", anthropic.SnapshotJSON(&req))
	}
	if req.Model == "" {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model is required"}}`, http.StatusBadRequest)
		return
	}

	if resp, ok := optim.Try(&req, s.cfg.Server); ok {
		sw := sse.NewWriter(w)
		builder := sse.NewBuilder(sw, newMessageID(), req.Model, resp.InputTokens)
		builder.Start()
		builder.EmitText(resp.Text)
		builder.SetOutputTokens(resp.OutputTokens)
		builder.Finish()
		return
	}

	resolved, err := s.router.Resolve(req.Model)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}

	if webtools.IsWebServerToolRequest(&req) {
		if !s.cfg.Server.EnableWebServerTools {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"tool_choice forces Anthropic web server tool, but local web server tools are disabled (enable_web_server_tools: false)"}}`, http.StatusBadRequest)
			return
		}
		sw := sse.NewWriter(w)
		builder := sse.NewBuilder(sw, newMessageID(), req.Model, estimateInputTokens(&req))
		policy := webtools.NewEgressPolicy(s.cfg.Server.WebFetchAllowedSchemes, s.cfg.Server.WebFetchAllowPrivateNetworks)
		webtools.StreamWebServerToolResponse(r.Context(), &req, builder, policy, webtools.DefaultRunner())
		return
	}

	if resolved.Provider.Kind == config.KindOpenAIChat && webtools.HasListedAnthropicWebServerTools(&req) && !s.cfg.Server.EnableWebServerTools {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"OpenAI Chat upstreams cannot use listed Anthropic web server tools (web_search / web_fetch) without the local web server tool handler"}}`, http.StatusBadRequest)
		return
	}

	adapter, err := s.adapterFor(resolved.ProviderName, resolved.Provider)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	log.Printf("messages: incoming=%s -> provider=%s upstream_model=%s", req.Model, resolved.ProviderName, resolved.Model)

	sw := sse.NewWriter(w)
	builder := sse.NewBuilder(sw, newMessageID(), req.Model, estimateInputTokens(&req))
	if err := adapter.Stream(r.Context(), &req, resolved.Model, builder); err != nil {
		log.Printf("messages: adapter error: %v", err)
	}
	if !builder.Finished() {
		builder.Finish()
	}
}

func authOK(r *http.Request, expected string) bool {
	if h := r.Header.Get("x-api-key"); h != "" && tokenEqual(h, expected) {
		return true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && h[:7] == "Bearer " && tokenEqual(h[7:], expected) {
			return true
		}
		if tokenEqual(h, expected) {
			return true
		}
	}
	return false
}

func tokenEqual(got, expected string) bool {
	gotSum := sha256.Sum256([]byte(got))
	expectedSum := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(gotSum[:], expectedSum[:]) == 1
}

func newMessageID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return "msg_" + hex.EncodeToString(buf[:])
}

func estimateInputTokens(req *anthropic.Request) int {
	total := 0
	if sys, err := anthropic.SystemText(req.System); err == nil {
		total += len(sys) / 4
	}
	for _, m := range req.Messages {
		for _, b := range m.Content.AsBlocks() {
			total += len(b.Text) / 4
			total += len(b.Thinking) / 4
			if len(b.Input) > 0 {
				total += len(b.Input) / 4
			}
			if len(b.Content) > 0 {
				total += len(b.Content) / 4
			}
		}
	}
	return total
}
