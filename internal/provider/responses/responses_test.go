package responses

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
)

type nopResponseWriter struct{}

func (nopResponseWriter) Header() http.Header         { return http.Header{} }
func (nopResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopResponseWriter) WriteHeader(statusCode int)  {}

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
	tools := body["tools"].([]toolDecl)
	if len(tools) != 1 || tools[0].Name != "bash" {
		t.Fatalf("tools = %+v", tools)
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
	if tools[0].Type != "function" || tools[0].Name != "bash" {
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
