package chat

import (
	"encoding/json"
	"testing"

	"github.com/relaycode/relaycode/internal/anthropic"
)

func TestConvertAssistantBlocksSplitsPostToolText(t *testing.T) {
	msgs, err := convertAssistantBlocks([]anthropic.Block{
		{Type: "text", Text: "before"},
		{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"x"}`)},
		{Type: "text", Text: "after"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].Content != "before" || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("first message = %+v", msgs[0])
	}
	if msgs[1].Content != "after" || len(msgs[1].ToolCalls) != 0 {
		t.Fatalf("second message = %+v", msgs[1])
	}
}
