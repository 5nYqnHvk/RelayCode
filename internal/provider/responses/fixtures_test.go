package responses

import (
	"bytes"
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

type responsesFixture struct {
	Name        string                     `json:"name"`
	Request     *anthropic.Request         `json:"request,omitempty"`
	Requests    []anthropic.Request        `json:"requests,omitempty"`
	Streams     []string                   `json:"streams,omitempty"`
	Config      responsesFixtureConfig     `json:"config,omitempty"`
	WantBody    responsesFixtureWantBody   `json:"want_body,omitempty"`
	WantBodies  []responsesFixtureWantBody `json:"want_bodies,omitempty"`
	WantOutput  []string                   `json:"want_output,omitempty"`
	WantOutputs [][]string                 `json:"want_outputs,omitempty"`
}

type responsesFixtureConfig struct {
	ExperimentalPreviousResponseID     bool `json:"experimental_previous_response_id,omitempty"`
	ExperimentalPassthroughServerTools bool `json:"experimental_passthrough_server_tools,omitempty"`
}

type responsesFixtureWantBody struct {
	PromptCacheKey           string                       `json:"prompt_cache_key,omitempty"`
	Reasoning                bool                         `json:"reasoning,omitempty"`
	ToolType                 string                       `json:"tool_type,omitempty"`
	ToolName                 string                       `json:"tool_name,omitempty"`
	PreviousResponseID       string                       `json:"previous_response_id,omitempty"`
	PreviousResponseIDAbsent bool                         `json:"previous_response_id_absent,omitempty"`
	Store                    *bool                        `json:"store,omitempty"`
	InputLen                 *int                         `json:"input_len,omitempty"`
	TailFunctionOutput       *responsesTailFunctionOutput `json:"tail_function_output,omitempty"`
}

type responsesTailFunctionOutput struct {
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func TestClaudeCodeResponsesFixtures(t *testing.T) {
	for _, dir := range []string{
		"text_reasoning_cache_key",
		"custom_tool_call",
		"function_call_arguments_done",
		"done_only_function_call",
		"custom_tool_call_input_delta",
		"previous_response_id_chain",
	} {
		fixture := loadResponsesFixture(t, dir)
		t.Run(fixture.Name, func(t *testing.T) {
			runResponsesFixture(t, dir, fixture)
		})
	}
}

func loadResponsesFixture(t *testing.T, dir string) responsesFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", dir, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture responsesFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func runResponsesFixture(t *testing.T, dir string, fixture responsesFixture) {
	t.Helper()
	requests := fixture.Requests
	if len(requests) == 0 && fixture.Request != nil {
		requests = []anthropic.Request{*fixture.Request}
	}
	if len(requests) == 0 {
		t.Fatal("fixture has no requests")
	}
	streams := fixture.Streams
	if len(streams) == 0 {
		streams = []string{"upstream.sse"}
	}
	var gotBodies []map[string]any
	var handlerErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		idx := len(gotBodies)
		if idx >= len(streams) {
			handlerErr = http.ErrMissingFile
			http.Error(w, "missing fixture stream", http.StatusInternalServerError)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotBodies = append(gotBodies, body)
		raw, err := loadFixtureStream(filepath.Join("testdata", dir, streams[idx]))
		if err != nil {
			handlerErr = err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	adapter := &Adapter{
		pc: config.ProviderConfig{
			BaseURL:                            server.URL,
			APIKey:                             "key",
			ExperimentalPreviousResponseID:     fixture.Config.ExperimentalPreviousResponseID,
			ExperimentalPassthroughServerTools: fixture.Config.ExperimentalPassthroughServerTools,
		},
		client: server.Client(),
	}
	adapter.SetSession(session.NewStore(time.Hour, 10), "openai")

	outputs := fixture.WantOutputs
	if len(outputs) == 0 && len(fixture.WantOutput) > 0 {
		outputs = [][]string{fixture.WantOutput}
	}
	for i := range requests {
		rw := &recordResponseWriter{}
		builder := sse.NewBuilder(sse.NewWriter(rw), "msg", "model", 1)
		if err := adapter.Stream(context.Background(), &requests[i], "gpt", builder); err != nil {
			t.Fatal(err)
		}
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
		if i < len(outputs) {
			assertResponseOutput(t, rw.body.String(), outputs[i])
		}
	}
	wantBodies := fixture.WantBodies
	if len(wantBodies) == 0 && fixture.WantBody.hasAssertions() {
		wantBodies = []responsesFixtureWantBody{fixture.WantBody}
	}
	if len(wantBodies) > 0 && len(gotBodies) != len(wantBodies) {
		t.Fatalf("request body count = %d, want %d", len(gotBodies), len(wantBodies))
	}
	for i, want := range wantBodies {
		assertResponsesBody(t, gotBodies[i], want)
	}
}

func loadFixtureStream(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return os.ReadFile(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		out.Write(raw)
	}
	return out.Bytes(), nil
}

func assertResponseOutput(t *testing.T, out string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %s:\n%s", want, out)
		}
	}
}

func (w responsesFixtureWantBody) hasAssertions() bool {
	return w.PromptCacheKey != "" || w.Reasoning || w.ToolType != "" || w.ToolName != "" || w.PreviousResponseID != "" || w.PreviousResponseIDAbsent || w.Store != nil || w.InputLen != nil || w.TailFunctionOutput != nil
}

func assertResponsesBody(t *testing.T, body map[string]any, want responsesFixtureWantBody) {
	t.Helper()
	if want.PromptCacheKey != "" && body["prompt_cache_key"] != want.PromptCacheKey {
		t.Fatalf("prompt_cache_key = %+v body=%+v", body["prompt_cache_key"], body)
	}
	if want.Reasoning {
		if _, ok := body["reasoning"].(map[string]any); !ok {
			t.Fatalf("reasoning missing: %+v", body)
		}
	}
	if want.ToolType != "" || want.ToolName != "" {
		tools := body["tools"].([]any)
		tool := tools[0].(map[string]any)
		if want.ToolType != "" && tool["type"] != want.ToolType {
			t.Fatalf("tool type = %+v body=%+v", tool["type"], body)
		}
		if want.ToolName != "" && tool["name"] != want.ToolName {
			t.Fatalf("tool name = %+v body=%+v", tool["name"], body)
		}
	}
	if want.PreviousResponseID != "" && body["previous_response_id"] != want.PreviousResponseID {
		t.Fatalf("previous_response_id = %+v body=%+v", body["previous_response_id"], body)
	}
	if want.PreviousResponseIDAbsent {
		if _, ok := body["previous_response_id"]; ok {
			t.Fatalf("previous_response_id unexpectedly present: %+v", body)
		}
	}
	if want.Store != nil && body["store"] != *want.Store {
		t.Fatalf("store = %+v body=%+v", body["store"], body)
	}
	if want.InputLen != nil {
		input := body["input"].([]any)
		if len(input) != *want.InputLen {
			t.Fatalf("input len = %d body=%+v", len(input), body)
		}
	}
	if want.TailFunctionOutput != nil {
		input := body["input"].([]any)
		item := input[len(input)-1].(map[string]any)
		if item["type"] != "function_call_output" || item["call_id"] != want.TailFunctionOutput.CallID || item["output"] != want.TailFunctionOutput.Output {
			t.Fatalf("tail function output = %+v body=%+v", item, body)
		}
	}
}
