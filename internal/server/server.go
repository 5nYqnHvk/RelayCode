// Package server implements the Anthropic-compatible HTTP ingress.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/config"
	"github.com/relaycode/relaycode/internal/provider"
	"github.com/relaycode/relaycode/internal/provider/chat"
	"github.com/relaycode/relaycode/internal/provider/responses"
	"github.com/relaycode/relaycode/internal/router"
	"github.com/relaycode/relaycode/internal/sse"
)

type Server struct {
	cfg      *config.Config
	router   *router.Router
	adapters map[string]provider.Adapter
	addr     string
}

func New(cfg *config.Config) (*Server, error) {
	for name, pc := range cfg.Providers {
		if pc.Kind != config.KindOpenAIChat && pc.Kind != config.KindOpenAIResponses {
			return nil, fmt.Errorf("provider %q: unsupported kind %q", name, pc.Kind)
		}
	}
	return &Server{
		cfg:      cfg,
		router:   router.New(cfg),
		adapters: map[string]provider.Adapter{},
		addr:     net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)),
	}, nil
}

func (s *Server) adapterFor(name string, pc config.ProviderConfig) (provider.Adapter, error) {
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
	default:
		return nil, fmt.Errorf("provider %q: unsupported kind %q", name, pc.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	s.adapters[name] = a
	return a, nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
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

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.cfg.Server.AuthToken != "" && !authOK(r, s.cfg.Server.AuthToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req anthropic.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model is required"}}`, http.StatusBadRequest)
		return
	}

	resolved, err := s.router.Resolve(req.Model)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`, err.Error()), http.StatusBadRequest)
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
	if h := r.Header.Get("x-api-key"); h != "" && h == expected {
		return true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && h[:7] == "Bearer " && h[7:] == expected {
			return true
		}
		if h == expected {
			return true
		}
	}
	return false
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
