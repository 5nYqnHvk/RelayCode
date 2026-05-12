package anthropic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestContentUnmarshalAndAsBlocks(t *testing.T) {
	var raw Content
	if err := json.Unmarshal([]byte(`"hello"`), &raw); err != nil {
		t.Fatal(err)
	}
	if got := raw.AsBlocks(); len(got) != 1 || got[0].Type != "text" || got[0].Text != "hello" {
		t.Fatalf("raw.AsBlocks() = %+v", got)
	}

	var blocks Content
	if err := json.Unmarshal([]byte(`[{"type":"text","text":"hi"}]`), &blocks); err != nil {
		t.Fatal(err)
	}
	if got := blocks.AsBlocks(); len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("blocks.AsBlocks() = %+v", got)
	}
}

func TestToolsForUpstreamFiltersAnthropicServerToolsByDefault(t *testing.T) {
	tools := []Tool{
		{Name: "bash", Type: "custom"},
		{Name: "web_search", Type: "custom"},
		{Name: "server", Type: "web_search_20250305"},
		{Name: "plain"},
	}

	got := ToolsForUpstream(tools, false)
	want := []Tool{{Name: "bash", Type: "custom"}, {Name: "web_search", Type: "custom"}, {Name: "plain"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolsForUpstream(false) = %+v, want %+v", got, want)
	}
	if got := ToolsForUpstream(tools, true); !reflect.DeepEqual(got, tools) {
		t.Fatalf("ToolsForUpstream(true) = %+v, want %+v", got, tools)
	}
}

func TestBlocksForUpstreamDegradesServerToolBlocks(t *testing.T) {
	blocks := []Block{
		{Type: "text", Text: "keep"},
		{Type: "server_tool_use", ID: "srv_1", Name: "web_search", Input: json.RawMessage(`{"query":"relaycode"}`)},
		{Type: "web_search_tool_result", ToolUseID: "srv_1", Content: json.RawMessage(`"result"`)},
		{Type: "tool_use", Name: "bash"},
	}

	got := BlocksForUpstream(blocks, false)
	if len(got) != 4 || got[0].Text != "keep" || got[3].Type != "tool_use" {
		t.Fatalf("BlocksForUpstream(false) = %+v", got)
	}
	if got[1].Type != "text" || !strings.Contains(got[1].Text, "web_search") || !strings.Contains(got[2].Text, "result") {
		t.Fatalf("server history not degraded: %+v", got)
	}
	stripped := StripServerToolBlocks(blocks)
	wantStripped := []Block{{Type: "text", Text: "keep"}, {Type: "tool_use", Name: "bash"}}
	if !reflect.DeepEqual(stripped, wantStripped) {
		t.Fatalf("StripServerToolBlocks = %+v, want %+v", stripped, wantStripped)
	}
	if got := BlocksForUpstream(blocks, true); !reflect.DeepEqual(got, blocks) {
		t.Fatalf("BlocksForUpstream(true) = %+v, want %+v", got, blocks)
	}
}

func TestSystemTextDropsVolatileBillingHeader(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"x-anthropic-billing-header: abc cch=random"},
		{"type":"text","text":"stable instructions"},
		{"type":"thinking","thinking":"skip"},
		{"type":"text","text":"more instructions"}
	]`)

	got, err := SystemText(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != "stable instructions\n\nmore instructions" {
		t.Fatalf("SystemText() = %q", got)
	}
}

func TestSessionIDPrefersSessionIDThenDeviceIDThenPlainUserID(t *testing.T) {
	tests := []struct {
		name string
		md   json.RawMessage
		want string
	}{
		{
			name: "session id",
			md:   json.RawMessage(`{"user_id":"{\"session_id\":\"sess-1\",\"device_id\":\"dev-1\"}"}`),
			want: "sess-1",
		},
		{
			name: "device id fallback",
			md:   json.RawMessage(`{"user_id":"{\"device_id\":\"dev-1\"}"}`),
			want: "dev-1",
		},
		{
			name: "plain user id",
			md:   json.RawMessage(`{"user_id":"plain-user"}`),
			want: "plain-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{Metadata: tt.md}
			if got := req.SessionID(); got != tt.want {
				t.Fatalf("SessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolResultTextFlattensTextBlocks(t *testing.T) {
	got, err := ToolResultText(json.RawMessage(`[{"type":"text","text":"one"},{"type":"image"},{"type":"text","text":"two"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "one\ntwo" {
		t.Fatalf("ToolResultText() = %q", got)
	}
}

func TestNormalizeMessagesForUpstreamRepairsTranscript(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: Content{Blocks: []Block{{Type: "tool_result", ToolUseID: "orphan", Content: json.RawMessage(`"old"`)}}}},
		{Role: "assistant", Content: Content{Blocks: []Block{{Type: "thinking", Thinking: "stale"}}}},
		{Role: "assistant", Content: Content{Blocks: []Block{{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/x"}`), Caller: json.RawMessage(`{"name":"ToolSearch"}`)}}}},
		{Role: "user", Content: Content{Blocks: []Block{{Type: "tool_result", ToolUseID: "call_2", Content: json.RawMessage(`[{"type":"tool_reference","tool_name":"Read"}]`)}}}},
		{Role: "assistant", Content: Content{Blocks: []Block{{Type: "text", Text: "done"}, {Type: "thinking", Thinking: "tail"}}}},
	}

	got := NormalizeMessagesForUpstream(msgs, false, false)
	if len(got) != 4 {
		t.Fatalf("normalized len = %d: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content.AsBlocks()[0].Type != "text" {
		t.Fatalf("orphan result was not replaced: %+v", got[0])
	}
	assistantTool := got[1].Content.AsBlocks()[0]
	if assistantTool.Type != "tool_use" || len(assistantTool.Caller) != 0 {
		t.Fatalf("assistant tool not preserved with caller stripped: %+v", assistantTool)
	}
	resultBlocks := got[2].Content.AsBlocks()
	if len(resultBlocks) != 1 || resultBlocks[0].Type != "tool_result" || resultBlocks[0].ToolUseID != "call_1" || !resultBlocks[0].IsError {
		t.Fatalf("missing synthetic tool_result: %+v", resultBlocks)
	}
	lastBlocks := got[3].Content.AsBlocks()
	if len(lastBlocks) != 1 || lastBlocks[0].Type != "text" || lastBlocks[0].Text != "done" {
		t.Fatalf("trailing thinking not stripped: %+v", lastBlocks)
	}
}

func TestRequestPreservesBetaAndExtraBodyFields(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"model":"claude","messages":[],"betas":["advanced-tool-use-2025-11-20"],"context_management":{"edits":[{"type":"clear"}]},"speed":"fast","output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}}}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.HasToolSearchBeta() || len(req.ContextManagement) == 0 || len(req.ExtraFields["speed"]) == 0 {
		t.Fatalf("request beta/body fields not captured: %+v", req)
	}
	if req.OutputConfig == nil || req.OutputConfig.RawField("format") == nil {
		t.Fatalf("output_config extra fields not captured: %+v", req.OutputConfig)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"betas"`, `"context_management"`, `"speed"`, `"format"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("marshaled request missing %s: %s", want, raw)
		}
	}
}
