package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	anthropictypes "github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
)

type recordResponseWriter struct {
	header http.Header
	body   strings.Builder
}

func (w *recordResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *recordResponseWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *recordResponseWriter) WriteHeader(statusCode int)  {}
func (w *recordResponseWriter) Flush()                      {}

func TestStreamPassesBetasHeaderAndBodyFields(t *testing.T) {
	stream, err := os.ReadFile("testdata/passthrough/upstream.sse")
	if err != nil {
		t.Fatal(err)
	}
	var betaHeader string
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		betaHeader = r.Header.Get("anthropic-beta")
		raw, _ := io.ReadAll(r.Body)
		requestBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(stream)
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropictypes.Request{
		Messages:          []anthropictypes.Message{{Role: "user", Content: anthropictypes.Content{Raw: "hi"}}},
		Betas:             []string{"advanced-tool-use-2025-11-20"},
		ContextManagement: json.RawMessage(`{"edits":[{"type":"clear"}]}`),
		ExtraFields:       map[string]json.RawMessage{"speed": json.RawMessage(`"fast"`)},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "claude", builder); err != nil {
		t.Fatal(err)
	}
	if betaHeader != "advanced-tool-use-2025-11-20" {
		t.Fatalf("anthropic-beta = %q", betaHeader)
	}
	for _, want := range []string{`"betas"`, `"context_management"`, `"speed":"fast"`} {
		if !strings.Contains(requestBody, want) {
			t.Fatalf("body missing %s: %s", want, requestBody)
		}
	}
}
