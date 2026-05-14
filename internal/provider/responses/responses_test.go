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
	"time"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/session"
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
	if len(tools) != 1 || tools[0].Name != "bash" || tools[0].Strict == nil || !*tools[0].Strict {
		t.Fatalf("tools = %+v", tools)
	}
	if body["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %v", body["parallel_tool_calls"])
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
	include := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %+v", include)
	}
}

func TestBuildRequestMapsOutputFormatToTextFormat(t *testing.T) {
	req := &anthropic.Request{OutputConfig: &anthropic.OutputConfig{ExtraFields: map[string]json.RawMessage{
		"format": json.RawMessage(`{"type":"json_schema","schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}}`),
	}}}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	text := body["text"].(map[string]any)
	format := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "relaycode_output_schema" || format["strict"] != true {
		t.Fatalf("text format = %+v", format)
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

func TestBuildRequestDeclaresSchemaLessCustomToolAsResponsesCustom(t *testing.T) {
	req := &anthropic.Request{
		Tools:      []anthropic.Tool{{Name: "apply_patch", Type: "custom", Description: "apply diff"}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"apply_patch"}`),
	}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]toolDecl)
	if len(tools) != 1 || tools[0].Type != "custom" || tools[0].Name != "apply_patch" || tools[0].Format["type"] != "text" || tools[0].Parameters != nil {
		t.Fatalf("tools = %+v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "custom" || choice["name"] != "apply_patch" {
		t.Fatalf("tool_choice = %+v", choice)
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
	if tools[0].Type != "function" || tools[0].Name != "bash" || tools[0].Strict == nil || *tools[0].Strict {
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

func TestBuildRequestMapsAnyToolChoiceToRequired(t *testing.T) {
	req := &anthropic.Request{ToolChoice: json.RawMessage(`{"type":"any"}`)}
	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if body["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %+v", body["tool_choice"])
	}
}

func TestBuildRequestAddsToolUseBridgeInstruction(t *testing.T) {
	req := &anthropic.Request{
		System: json.RawMessage(`"base instructions"`),
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}
	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	instructions := body["instructions"].(string)
	if !strings.Contains(instructions, "base instructions") || !strings.Contains(instructions, toolUseBridgeInstruction) {
		t.Fatalf("instructions = %q", instructions)
	}
}

func TestBuildRequestSkipsToolBridgeWhenToolChoiceNone(t *testing.T) {
	req := &anthropic.Request{
		System:     json.RawMessage(`"base instructions"`),
		ToolChoice: json.RawMessage(`{"type":"none"}`),
		Tools: []anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}
	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	instructions := body["instructions"].(string)
	if strings.Contains(instructions, toolUseBridgeInstruction) {
		t.Fatalf("instructions = %q", instructions)
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

func TestStreamDoesNotExecuteHeuristicTextToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"● <function=Read><parameter=file_path>/tmp/x</parameter>"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
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

func TestStreamDropsUnknownFunctionCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Write","arguments":"{\"file_path\":\"/tmp/x\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2},"output":[{"type":"function_call"}]}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "call tool"}}},
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

func TestStreamDropsInvalidFunctionCallArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"file_path\":123}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2},"output":[{"type":"function_call"}]}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "call tool"}}},
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

func TestStreamRetriesAfterInvalidArgumentsDonePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Read"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.function_call_arguments.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"file_path\":123}"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"file_path\":\"/tmp/x\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
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

func TestStreamEmitsFunctionCallArgumentsDonePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Read"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.function_call_arguments.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"file_path\":\"/tmp/x\"}"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
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

func TestStreamChainsPreviousResponseIDWhenExperimental(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		ids := []string{"resp_1", "resp_2", "resp_3"}
		id := ids[len(bodies)-1]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.completed
` + `data: {"type":"response.completed","response":{"id":"` + id + `","usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key", ExperimentalPreviousResponseID: true}, client: server.Client()}
	adapter.SetSession(session.NewStore(time.Hour, 10), "openai")
	first := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "first"}}}}
	second := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "first"}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "reply"}, {Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/a"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok 1"`)}}}},
	}}
	third := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "first"}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "reply"}, {Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/a"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok 1"`)}}}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "next"}, {Type: "tool_use", ID: "call_2", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/b"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_2", Content: json.RawMessage(`"ok 2"`)}}}},
	}}

	for _, req := range []*anthropic.Request{first, second, third} {
		rw := &recordResponseWriter{}
		builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
		if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("bodies len = %d", len(bodies))
	}
	if _, ok := bodies[0]["previous_response_id"]; ok {
		t.Fatalf("first request unexpectedly chained: %+v", bodies[0])
	}
	assertFunctionOutputTail := func(body map[string]any, prev, callID, output string) {
		t.Helper()
		if body["previous_response_id"] != prev {
			t.Fatalf("previous_response_id = %+v body=%+v", body["previous_response_id"], body)
		}
		input := body["input"].([]any)
		if len(input) != 1 {
			t.Fatalf("tail input = %+v", input)
		}
		item := input[0].(map[string]any)
		if item["type"] != "function_call_output" || item["call_id"] != callID || item["output"] != output || body["store"] != true {
			t.Fatalf("tail request body = %+v", body)
		}
	}
	assertFunctionOutputTail(bodies[1], "resp_1", "call_1", "ok 1")
	assertFunctionOutputTail(bodies[2], "resp_2", "call_2", "ok 2")
}

func TestStreamDoesNotUsePreviousResponseIDByDefault(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		id := "resp_1"
		if len(bodies) == 2 {
			id = "resp_2"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.completed
` + `data: {"type":"response.completed","response":{"id":"` + id + `","usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	adapter.SetSession(session.NewStore(time.Hour, 10), "openai")
	first := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "first"}}}}
	second := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "first"}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "reply"}, {Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/a"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok 1"`)}}}},
	}}

	for _, req := range []*anthropic.Request{first, second} {
		rw := &recordResponseWriter{}
		builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
		if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies len = %d", len(bodies))
	}
	if _, ok := bodies[1]["previous_response_id"]; ok {
		t.Fatalf("previous_response_id unexpectedly present: %+v", bodies[1])
	}
	if bodies[1]["store"] != false {
		t.Fatalf("default HTTP replay should keep store false, body = %+v", bodies[1])
	}
	if len(bodies[1]["input"].([]any)) != 4 {
		t.Fatalf("default HTTP replay should include full input, body = %+v", bodies[1])
	}
}

func TestBuildRequestMapsImageBlocks(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{
		{Type: "text", Text: "look"},
		{Type: "image", Source: &anthropic.ImageSource{Type: "base64", MediaType: "image/webp", Data: "AAA"}},
		{Type: "text", Text: "now"},
	}}}}}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]inputItem)
	if len(items) != 1 {
		t.Fatalf("input = %+v", items)
	}
	parts := items[0].Content
	if len(parts) != 3 || parts[0].Text != "look" || parts[1].Type != "input_image" || parts[1].ImageURL != "data:image/webp;base64,AAA" || parts[2].Text != "now" {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestBuildRequestFlushesImageMessageBeforeToolResult(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"x"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{
			{Type: "image", Source: &anthropic.ImageSource{Type: "base64", MediaType: "image/gif", Data: "BBB"}},
			{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok"`)},
		}}},
	}}

	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]inputItem)
	if len(items) != 3 || items[0].Type != "function_call" || items[1].Type != "message" || items[2].Type != "function_call_output" || items[2].Output != "ok" {
		t.Fatalf("input = %+v", items)
	}
}

func TestBuildRequestRejectsInvalidImageBlock(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "image", Source: &anthropic.ImageSource{Type: "url", MediaType: "image/png", Data: "AAA"}}}}}}}
	_, err := buildRequest(req, "gpt", false)
	if err == nil || !strings.Contains(err.Error(), "unsupported image source type") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildRequestCompactsToolResultsWhenEnabled(t *testing.T) {
	longOutput := strings.Repeat("line\n", 300)
	raw, err := json.Marshal(longOutput)
	if err != nil {
		t.Fatal(err)
	}
	req := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_use", ID: "call_1", Name: "Bash", Input: json.RawMessage(`{"cmd":"yes"}`)}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: raw}}}},
	}}

	body, _, err := buildRequestWithOptions(req, "gpt", true, true, "")
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]inputItem)
	if len(items) != 2 {
		t.Fatalf("input = %+v", items)
	}
	output := items[1].Output
	if !strings.Contains(output, "tool output compacted") || len(output) >= len(longOutput) {
		t.Fatalf("tool result not compacted: len=%d output=%q", len(output), output)
	}
}

func TestBuildRequestReplaysCustomToolCallAndOutput(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{{Name: "apply_patch", Type: "custom"}},
		Messages: []anthropic.Message{
			{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_use", ID: "call_1", Name: "apply_patch", Input: json.RawMessage(`{"input":"*** Begin"}`)}}}},
			{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok"`)}}}},
		},
	}
	body, err := buildRequest(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	items := body["input"].([]inputItem)
	if len(items) != 2 {
		t.Fatalf("input = %+v", items)
	}
	if items[0].Type != "custom_tool_call" || items[0].Input != "*** Begin" || items[0].Args != "" {
		t.Fatalf("custom call item = %+v", items[0])
	}
	if items[1].Type != "custom_tool_call_output" || items[1].Output != "ok" {
		t.Fatalf("custom output item = %+v", items[1])
	}
}

func TestBuildRequestUsesFunctionModeForCustomTools(t *testing.T) {
	req := &anthropic.Request{
		Tools: []anthropic.Tool{{Name: "apply_patch", Type: "custom"}},
		Messages: []anthropic.Message{
			{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_use", ID: "call_1", Name: "apply_patch", Input: json.RawMessage(`{"input":"*** Begin"}`)}}}},
			{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"ok"`)}}}},
		},
	}
	body, _, err := buildRequestWithOptions(req, "gpt", false, false, "function")
	if err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]toolDecl)
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Name != "apply_patch" || !*tools[0].Strict {
		t.Fatalf("tools = %+v", tools)
	}
	if !strings.Contains(string(tools[0].Parameters), `"input"`) {
		t.Fatalf("parameters = %s", tools[0].Parameters)
	}
	items := body["input"].([]inputItem)
	if len(items) != 2 {
		t.Fatalf("input = %+v", items)
	}
	if items[0].Type != "function_call" || items[0].Args != `{"input":"*** Begin"}` {
		t.Fatalf("function-mode call item = %+v", items[0])
	}
	if items[1].Type != "function_call_output" || items[1].Output != "ok" {
		t.Fatalf("function-mode output item = %+v", items[1])
	}
}

func TestStreamEmitsCustomToolCallInputDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: response.output_item.added
` + `data: {"type":"response.output_item.added","item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch"}}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.custom_tool_call_input.delta
` + `data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","call_id":"call_1","delta":"*** Begin"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_item.done
` + `data: {"type":"response.output_item.done","item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch"}}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.completed
` + `data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "patch"}}},
		Tools:    []anthropic.Tool{{Name: "apply_patch", Type: "custom"}},
	}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"apply_patch"`, `"partial_json":"{\"input\":\"*** Begin\"}"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %s:\n%s", want, out)
		}
	}
}

func TestResponsesHelperEdges(t *testing.T) {
	for _, errText := range []string{"previous_response_id not found", "previous response expired", "not found"} {
		if !isInvalidPreviousResponseError(errString(errText)) {
			t.Fatalf("expected invalid previous response: %q", errText)
		}
	}
	if isInvalidPreviousResponseError(nil) || isInvalidPreviousResponseError(errString("rate limited")) {
		t.Fatal("unexpected invalid previous response classification")
	}
	if got := normalizeToolArgsForAnthropic("function_call", ""); got != "{}" {
		t.Fatalf("empty function args = %q", got)
	}
	if got := normalizeToolArgsForAnthropic("custom_tool_call", "raw text"); got != `{"input":"raw text"}` {
		t.Fatalf("custom args = %q", got)
	}
	if got := normalizeToolArgsForAnthropic("custom_tool_call", `{"x":1}`); got != `{"x":1}` {
		t.Fatalf("custom JSON args = %q", got)
	}
	if got := customToolInput(`{"input":"patch"}`); got != "patch" {
		t.Fatalf("customToolInput = %q", got)
	}
	if !isCallableItem("function_call") || !isCallableItem("custom_tool_call") || isCallableItem("message") {
		t.Fatal("isCallableItem mismatch")
	}
}

func TestResponsesTextFormatErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"bad json", json.RawMessage(`{`), "output_config.format"},
		{"missing type", json.RawMessage(`{}`), "type is required"},
		{"schema missing", json.RawMessage(`{"type":"json_schema"}`), "schema is required"},
		{"unsupported", json.RawMessage(`{"type":"xml"}`), "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := responsesTextFormat(&anthropic.Request{OutputConfig: &anthropic.OutputConfig{ExtraFields: map[string]json.RawMessage{"format": tc.raw}}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestStreamHandlesResponsesErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.error
` + `data: {"type":"response.error","error":{"message":"bad upstream"}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hi"}}}}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	out := rw.body.String()
	if !strings.Contains(out, "bad upstream") || !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Fatalf("output = %s", out)
	}
	if builder.ErrorMessage() != "bad upstream" {
		t.Fatalf("error message = %q", builder.ErrorMessage())
	}
}

func TestStreamRetriesTransportErrorBeforeContent(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("data: {"))
			return
		}
		_, _ = w.Write([]byte(`event: response.completed
` + `data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key", MaxRetries: 1}, client: server.Client()}
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hi"}}}}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	out := rw.body.String()
	if strings.Contains(out, "unexpected EOF") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("output = %s", out)
	}
	if builder.ErrorMessage() != "" {
		t.Fatalf("error message = %q", builder.ErrorMessage())
	}
}

func TestStreamRetriesRetriableResponseErrorBeforeContent(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte(`event: response.error
` + `data: {"type":"response.error","error":{"message":"upstream busy, try again"}}` + "\n\n"))
			return
		}
		_, _ = w.Write([]byte(`event: response.completed
` + `data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key", MaxRetries: 1}, client: server.Client()}
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hi"}}}}
	rw := &recordResponseWriter{}
	builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
	if err := adapter.Stream(context.Background(), req, "gpt", builder); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	out := rw.body.String()
	if strings.Contains(out, "upstream busy") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("output = %s", out)
	}
	if builder.ErrorMessage() != "" {
		t.Fatalf("error message = %q", builder.ErrorMessage())
	}
}

type errString string

func (e errString) Error() string { return string(e) }
