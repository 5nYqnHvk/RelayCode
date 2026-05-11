package webtools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/sse"
)

type captureWriter struct{ b strings.Builder }

func (w *captureWriter) Header() http.Header         { return http.Header{} }
func (w *captureWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
func (w *captureWriter) WriteHeader(statusCode int)  {}

func TestStreamWebSearchServerToolResponse(t *testing.T) {
	req := &anthropic.Request{
		Model:      "claude",
		Messages:   []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "Perform a web search for the query: DeepSeek V4"}}},
		Tools:      []anthropic.Tool{{Name: "web_search", Type: "web_search_20250305"}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"web_search"}`),
	}
	cw := &captureWriter{}
	builder := sse.NewBuilder(sse.NewWriter(cw), "msg", "claude", 42)
	runner := Runner{Search: func(context.Context, string) ([]SearchResult, error) {
		return []SearchResult{{Title: "DeepSeek V4 Released", URL: "https://example.com/v4"}}, nil
	}}
	StreamWebServerToolResponse(context.Background(), req, builder, NewEgressPolicy("http,https", false), runner)
	out := cw.b.String()
	for _, want := range []string{"server_tool_use", "web_search_tool_result", "https://example.com/v4", "web_search_requests"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
}
