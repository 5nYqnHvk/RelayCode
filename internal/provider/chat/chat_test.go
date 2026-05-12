package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
)

func TestBuildRequestFiltersServerToolsByDefault(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "web_search", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]openaiTool)
	if len(tools) != 1 || tools[0].Function.Name != "bash" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestBuildRequestMapsOutputConfigEffort(t *testing.T) {
	req := &anthropic.Request{OutputConfig: &anthropic.OutputConfig{Effort: "max"}}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %v", body["reasoning_effort"])
	}
}

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

func TestBuildRequestPassesServerToolsWhenExperimental(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "web_search", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []anthropic.Message{
			{
				Role: "assistant",
				Content: anthropic.Content{Blocks: []anthropic.Block{
					{Type: "server_tool_use", ID: "call_1", Name: "web_search", Input: json.RawMessage(`{"query":"relaycode"}`)},
				}},
			},
			{
				Role: "user",
				Content: anthropic.Content{Blocks: []anthropic.Block{
					{Type: "web_search_tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"result text"`)},
				}},
			},
		},
	}

	body, err := buildRequest(req, "gpt", true)
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]openaiTool)
	if len(tools) != 2 || tools[1].Function.Name != "web_search" {
		t.Fatalf("tools = %+v", tools)
	}
	messages := body["messages"].([]openaiMessage)
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	if len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("assistant message = %+v", messages[0])
	}
	if messages[1].Role != "tool" || messages[1].ToolCallID != "call_1" || messages[1].Content != "result text" {
		t.Fatalf("tool result message = %+v", messages[1])
	}
}

func TestStreamEmitsValidNativeToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":"}}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/tmp/x\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "read"}}},
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"Read"`, `"partial_json":"{\"file_path\":\"/tmp/x\"}"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %s:\n%s", want, out)
		}
	}
}

func TestStreamDoesNotExecuteHeuristicTextToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"● <function=Read><parameter=file_path>/tmp/x</parameter>"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "read"}}},
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	if strings.Contains(out, `"type":"tool_use"`) || strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("heuristic text executed as tool:\n%s", out)
	}
}

func TestStreamDropsUnknownNativeToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Write","arguments":"{\"file_path\":\"/tmp/x\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "write"}}},
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	if strings.Contains(out, `"type":"tool_use"`) || strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("unknown tool executed:\n%s", out)
	}
}

func TestStreamDropsInvalidNativeToolCallArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":123}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "read"}}},
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`),
		}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	if strings.Contains(out, `"type":"tool_use"`) || strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("invalid args executed:\n%s", out)
	}
}
