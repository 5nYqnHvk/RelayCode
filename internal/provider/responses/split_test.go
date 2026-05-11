package responses

import (
	"encoding/json"
	"testing"

	"github.com/relaycode/relaycode/internal/anthropic"
)

func TestConvertMessagesToItemsSplitsPostToolText(t *testing.T) {
	req := []anthropic.Message{{
		Role: "assistant",
		Content: anthropic.Content{Blocks: []anthropic.Block{
			{Type: "text", Text: "before"},
			{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"x"}`)},
			{Type: "text", Text: "after"},
		}},
	}}
	items, err := convertMessagesToItems(req, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Type != "message" || items[0].Content[0].Text != "before" {
		t.Fatalf("first item = %+v", items[0])
	}
	if items[1].Type != "function_call" || items[1].Name != "Read" {
		t.Fatalf("second item = %+v", items[1])
	}
	if items[2].Type != "message" || items[2].Content[0].Text != "after" {
		t.Fatalf("third item = %+v", items[2])
	}
}
