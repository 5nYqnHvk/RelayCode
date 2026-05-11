package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
)

type nopResponseWriter struct{}

func (nopResponseWriter) Header() http.Header         { return http.Header{} }
func (nopResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopResponseWriter) WriteHeader(statusCode int)  {}

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

func TestBuildRequestFiltersServerToolsByDefault(t *testing.T) {
	strict := true
	req := &anthropic.Request{
		Tools: []anthropic.Tool{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strict},
			{Name: "web_search", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]toolDecl)
	if len(tools) != 1 || tools[0].Name != "bash" || !tools[0].Strict {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestBuildRequestMapsOutputConfigEffort(t *testing.T) {
	req := &anthropic.Request{OutputConfig: &anthropic.OutputConfig{Effort: "max"}}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning.effort = %v", reasoning["effort"])
	}
}

func TestBuildRequestOmitsTemperature(t *testing.T) {
	temp := 1.0
	req := &anthropic.Request{Temperature: &temp}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["temperature"]; ok {
		t.Fatalf("temperature should be omitted for responses payload: %+v", body)
	}
}

func TestBuildRequestAliasesTypeArgument(t *testing.T) {
	req := &anthropic.Request{Tools: []anthropic.Tool{{
		Name:        "NotionLike",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string"},"name":{"type":"string"}},"required":["type"]}`),
	}}}
	body, aliases, err := buildRequestWithAliases(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if aliases["NotionLike"]["_fcc_arg_type"] != "type" {
		t.Fatalf("aliases = %+v", aliases)
	}
	tools := body["tools"].([]toolDecl)
	var params map[string]any
	if err := json.Unmarshal(tools[0].Parameters, &params); err != nil {
		t.Fatal(err)
	}
	props := params["properties"].(map[string]any)
	if _, ok := props["type"]; ok {
		t.Fatalf("schema still has type property: %+v", props)
	}
	if _, ok := props["_fcc_arg_type"]; !ok {
		t.Fatalf("schema missing alias property: %+v", props)
	}
}

func TestBuildRequestPassesServerToolsAsFunctionsWhenExperimental(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "web_search", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	body, err := buildRequest(req, "gpt", true)
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]toolDecl)
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Type != "function" || tools[0].Name != "bash" || tools[0].Strict {
		t.Fatalf("function tool = %+v", tools[0])
	}
	if tools[1].Type != "function" || tools[1].Name != "web_search" || tools[1].ExternalWebAccess != nil {
		t.Fatalf("web search passthrough tool = %+v", tools[1])
	}
}

func TestBuildRequestMapsForcedFunctionToolChoice(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"bash"}`),
	}

	body, err := buildRequest(req, "gpt", true)
	if err != nil {
		t.Fatal(err)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "bash" {
		t.Fatalf("tool_choice = %+v", choice)
	}
}

func TestCodexAuthHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	body := `{"tokens":{"access_token":"access-token","account_id":"account-id"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	headers, err := codexAuthHeaders(path)
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer access-token" || headers["ChatGPT-Account-ID"] != "account-id" {
		t.Fatalf("headers = %+v", headers)
	}
}

func TestEmitWebSearchCallEmitsAnthropicServerToolBlocks(t *testing.T) {
	b := sse.NewBuilder(sse.NewWriter(nopResponseWriter{}), "msg", "model", 0)
	b.Start()
	handled, err := emitWebSearchCall(`{"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"discord"}}}`, b)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("web_search_call was not handled")
	}
	b.Finish()
}

func TestBuildRequestDropsWebSearchReplayItems(t *testing.T) {
	req := &anthropic.Request{
		Messages: []anthropic.Message{
			{
				Role: "assistant",
				Content: anthropic.Content{Blocks: []anthropic.Block{
					{Type: "server_tool_use", ID: "call_1", Name: "web_search", Input: json.RawMessage(`{"query":"relaycode"}`)},
					{Type: "tool_use", ID: "call_2", Name: "bash", Input: json.RawMessage(`{"cmd":"pwd"}`)},
				}},
			},
			{
				Role: "user",
				Content: anthropic.Content{Blocks: []anthropic.Block{
					{Type: "web_search_tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"result text"`)},
					{Type: "tool_result", ToolUseID: "call_2", Content: json.RawMessage(`"/repo"`)},
				}},
			},
		},
	}

	body, err := buildRequest(req, "gpt", true)
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]inputItem)
	if len(items) != 2 {
		t.Fatalf("input = %+v", items)
	}
	if items[0].Type != "function_call" || items[0].Name != "bash" || items[0].CallID != "call_2" {
		t.Fatalf("function call item = %+v", items[0])
	}
	if items[1].Type != "function_call_output" || items[1].CallID != "call_2" || items[1].Output != "/repo" {
		t.Fatalf("function output item = %+v", items[1])
	}
}

func TestStreamEmitsDoneOnlyFunctionCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"NotionLike","arguments":"{\"_fcc_arg_type\":\"page\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2},"output":[{"type":"function_call"}]}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "call tool"}}},
		Tools: []anthropic.Tool{{
			Name:        "NotionLike",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string"}},"required":["type"]}`),
		}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"NotionLike"`, `"partial_json":"{\"type\":\"page\"}"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %s:\n%s", want, out)
		}
	}
}
