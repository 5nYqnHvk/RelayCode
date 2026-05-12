package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/session"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
)

type responsesFixture struct {
	name       string
	req        *anthropic.Request
	stream     string
	wantBody   func(t *testing.T, body map[string]any)
	wantOutput []string
}

func TestClaudeCodeResponsesFixtures(t *testing.T) {
	fixtures := []responsesFixture{
		{
			name: "text reasoning and cache key",
			req: &anthropic.Request{
				Metadata:     json.RawMessage(`{"user_id":"{\"session_id\":\"sess-1\"}"}`),
				Messages:     []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hello"}}},
				OutputConfig: &anthropic.OutputConfig{Effort: "high"},
			},
			stream: `event: response.reasoning_text.delta
` + `data: {"type":"response.reasoning_text.delta","delta":"think"}

` + `event: response.output_text.delta
` + `data: {"type":"response.output_text.delta","delta":"answer"}

` + `event: response.completed
` + `data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":8}}}}

`,
			wantBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["prompt_cache_key"] != "sess-1" {
					t.Fatalf("prompt_cache_key = %+v body=%+v", body["prompt_cache_key"], body)
				}
				if _, ok := body["reasoning"].(map[string]any); !ok {
					t.Fatalf("reasoning missing: %+v", body)
				}
			},
			wantOutput: []string{`"type":"thinking_delta"`, `"text":"answer"`},
		},
		{
			name: "custom tool call",
			req: &anthropic.Request{
				Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "patch"}}},
				Tools:    []anthropic.Tool{{Name: "apply_patch", Type: "custom"}},
			},
			stream: `event: response.output_item.added
` + `data: {"type":"response.output_item.added","item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch"}}

` + `event: response.custom_tool_call_input.delta
` + `data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","call_id":"call_1","delta":"*** Begin"}

` + `event: response.output_item.done
` + `data: {"type":"response.output_item.done","item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_1","name":"apply_patch"}}

` + `event: response.completed
` + `data: {"type":"response.completed","response":{"id":"resp_2","usage":{"input_tokens":1,"output_tokens":1}}}

`,
			wantBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				tools := body["tools"].([]any)
				tool := tools[0].(map[string]any)
				if tool["type"] != "custom" || tool["name"] != "apply_patch" {
					t.Fatalf("custom tool not declared: %+v", body)
				}
			},
			wantOutput: []string{`"type":"tool_use"`, `"name":"apply_patch"`, `"partial_json":"{\"input\":\"*** Begin\"}"`},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(strings.ReplaceAll(fixture.stream, `\n`, "\n")))
			}))
			defer server.Close()

			adapter := &Adapter{pc: config.ProviderConfig{BaseURL: server.URL, APIKey: "key"}, client: server.Client()}
			adapter.SetSession(session.NewStore(0, 10), "openai")
			rw := &recordResponseWriter{}
			builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
			if err := adapter.Stream(context.Background(), fixture.req, "gpt", builder); err != nil {
				t.Fatal(err)
			}
			fixture.wantBody(t, gotBody)
			out := rw.body.String()
			for _, want := range fixture.wantOutput {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %s:\n%s", want, out)
				}
			}
		})
	}
}
