package chat

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
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
