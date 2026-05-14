package responses

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
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
	items, err := convertMessagesToItems(req, true, nil, nil, false)
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

func TestConvertMessagesToItemsReplaysMultipleToolResults(t *testing.T) {
	req := []anthropic.Message{
		{
			Role: "assistant",
			Content: anthropic.Content{Blocks: []anthropic.Block{
				{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"file_path":"a"}`)},
				{Type: "tool_use", ID: "call_2", Name: "Glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
			}},
		},
		{
			Role: "user",
			Content: anthropic.Content{Blocks: []anthropic.Block{
				{Type: "tool_result", ToolUseID: "call_1", Content: json.RawMessage(`"file text"`)},
				{Type: "tool_result", ToolUseID: "call_2", Content: json.RawMessage(`[{"type":"text","text":"main.go"}]`)},
			}},
		},
	}
	items, err := convertMessagesToItems(req, true, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Type != "function_call" || items[0].CallID != "call_1" || items[1].Type != "function_call" || items[1].CallID != "call_2" {
		t.Fatalf("function calls = %+v", items[:2])
	}
	if items[2].Type != "function_call_output" || items[2].Output != "file text" || items[3].Type != "function_call_output" || items[3].Output != "main.go" {
		t.Fatalf("function outputs = %+v", items[2:])
	}
}
