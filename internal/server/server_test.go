package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
)

func TestHandleModelsListsConfiguredRoutes(t *testing.T) {
	srv, err := New(&config.Config{
		Server: config.ServerConfig{AuthToken: "token"},
		Routes: []config.Route{
			{Match: "opus", Provider: "responses", Model: "gpt-strong"},
			{Match: "sonnet", Provider: "responses", Model: "gpt-strong"},
			{Match: "*", Provider: "chat", Model: "gpt-fallback"},
		},
		Providers: map[string]config.ProviderConfig{
			"responses": {Kind: config.KindOpenAIResponses, BaseURL: "https://example.test/v1"},
			"chat":      {Kind: config.KindOpenAIChat, BaseURL: "https://example.test/v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("x-api-key", "token")
	rw := httptest.NewRecorder()
	srv.handleModels(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if body.Data[0].ID != "gpt-strong" || body.Data[0].OwnedBy != "responses" || body.Data[1].ID != "gpt-fallback" {
		t.Fatalf("models = %+v", body.Data)
	}
}

func TestHandleModelsRequiresAuth(t *testing.T) {
	srv, err := New(&config.Config{
		Server: config.ServerConfig{AuthToken: "token"},
		Routes: []config.Route{{Match: "*", Provider: "chat", Model: "gpt"}},
		Providers: map[string]config.ProviderConfig{
			"chat": {Kind: config.KindOpenAIChat, BaseURL: "https://example.test/v1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rw := httptest.NewRecorder()
	srv.handleModels(rw, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
}

func TestHandleModelsMethodVariants(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{}, config.KindOpenAIChat)
	for _, method := range []string{http.MethodHead, http.MethodOptions} {
		rw := httptest.NewRecorder()
		srv.handleModels(rw, httptest.NewRequest(method, "/v1/models", nil))
		if rw.Code != http.StatusOK {
			t.Fatalf("%s status = %d", method, rw.Code)
		}
		if got := rw.Header().Get("Allow"); got != "GET, HEAD, OPTIONS" {
			t.Fatalf("Allow = %q", got)
		}
	}
	rw := httptest.NewRecorder()
	srv.handleModels(rw, httptest.NewRequest(http.MethodPost, "/v1/models", nil))
	if rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rw.Code)
	}
}

func TestHandleStatsRequiresGetAndAuth(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{AuthToken: "token"}, config.KindOpenAIResponses)
	rw := httptest.NewRecorder()
	srv.handleStats(rw, httptest.NewRequest(http.MethodPost, "/debug/stats", nil))
	if rw.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rw.Code)
	}

	rw = httptest.NewRecorder()
	srv.handleStats(rw, httptest.NewRequest(http.MethodGet, "/debug/stats", nil))
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", rw.Code)
	}
}

func TestHandleStatsReturnsCountersAndSessions(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{AuthToken: "token"}, config.KindOpenAIResponses)
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hi"}}}}
	lookup, err := srv.sessions.Prepare("p", "upstream", req)
	if err != nil {
		t.Fatal(err)
	}
	srv.sessions.Commit(lookup, "p", "upstream", 1, "resp_1", 11, 7)
	srv.sessions.Stats.Hits.Add(2)

	hreq := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	hreq.Header.Set("Authorization", "Bearer token")
	rw := httptest.NewRecorder()
	srv.handleStats(rw, hreq)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
	var body struct {
		Sessions []struct {
			Provider      string `json:"provider"`
			UpstreamModel string `json:"upstream_model"`
			MessageCount  int    `json:"message_count"`
			ResponseID    string `json:"response_id"`
		} `json:"sessions"`
		Counters map[string]int64 `json:"counters"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].ResponseID != "resp_1" || body.Sessions[0].Provider != "p" {
		t.Fatalf("sessions = %#v", body.Sessions)
	}
	if body.Counters["hits"] != 2 {
		t.Fatalf("counters = %#v", body.Counters)
	}
}

func TestHandleMessagesValidationErrors(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{AuthToken: "token"}, config.KindOpenAIChat)

	cases := []struct {
		name   string
		method string
		body   string
		auth   string
		want   int
		needle string
	}{
		{"method", http.MethodGet, `{}`, "token", http.StatusMethodNotAllowed, "method not allowed"},
		{"auth", http.MethodPost, `{}`, "", http.StatusUnauthorized, "unauthorized"},
		{"json", http.MethodPost, `{`, "token", http.StatusBadRequest, "invalid_request_error"},
		{"model", http.MethodPost, `{"messages":[]}`, "token", http.StatusBadRequest, "model is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/v1/messages", strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("x-api-key", tc.auth)
			}
			rw := httptest.NewRecorder()
			srv.handleMessages(rw, req)
			if rw.Code != tc.want {
				t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
			}
			if !strings.Contains(rw.Body.String(), tc.needle) {
				t.Fatalf("body missing %q: %s", tc.needle, rw.Body.String())
			}
		})
	}
}

func TestHandleMessagesRejectsTooLargeBody(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{}, config.KindOpenAIChat)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", maxRequestBodyBytes+1)))
	rw := httptest.NewRecorder()
	srv.handleMessages(rw, req)
	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
}

func TestHandleMessagesOptimizedPath(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{EnableNetworkProbeMock: true}, config.KindOpenAIChat)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":1,"messages":[{"role":"user","content":"quota?"}]}`))
	rw := httptest.NewRecorder()
	srv.handleMessages(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
	out := rw.Body.String()
	for _, want := range []string{"event: message_start", "Quota check passed.", "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestHandleMessagesRejectsDisabledWebServerTools(t *testing.T) {
	srv := newTestServer(t, config.ServerConfig{EnableWebServerTools: false}, config.KindOpenAIChat)
	body := `{"model":"claude","messages":[{"role":"user","content":"search"}],"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rw := httptest.NewRecorder()
	srv.handleMessages(rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "local web server tools are disabled") {
		t.Fatalf("body = %s", rw.Body.String())
	}
}

func TestAuthOKAcceptsSupportedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		value  string
	}{
		{"x-api-key", "x-api-key", "token"},
		{"bearer", "Authorization", "Bearer token"},
		{"raw auth", "Authorization", "token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(tc.header, tc.value)
			if !authOK(req, "token") {
				t.Fatal("auth rejected")
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if authOK(req, "token") {
		t.Fatal("wrong token accepted")
	}
}

func TestEstimateInputTokensIncludesSystemAndBlocks(t *testing.T) {
	req := &anthropic.Request{
		System: json.RawMessage(`"12345678"`),
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{
			{Type: "text", Text: "12345678"},
			{Type: "thinking", Thinking: "1234"},
			{Type: "tool_use", Input: json.RawMessage(`{"x":"12345678"}`)},
			{Type: "tool_result", Content: json.RawMessage(`"12345678"`)},
		}}}},
	}
	if got := estimateInputTokens(req); got < 7 {
		t.Fatalf("tokens = %d", got)
	}
}

func newTestServer(t *testing.T, serverCfg config.ServerConfig, kind config.ProviderKind) *Server {
	t.Helper()
	if serverCfg.Host == "" {
		serverCfg.Host = "127.0.0.1"
	}
	cfg := &config.Config{
		Server: serverCfg,
		Routes: []config.Route{{Match: "claude", Provider: "p", Model: "upstream"}, {Match: "*", Provider: "p", Model: "fallback"}},
		Providers: map[string]config.ProviderConfig{
			"p": {Kind: kind, BaseURL: "https://example.test/v1", APIKey: "key"},
		},
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}
