package webtools

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
)

func TestWebServerToolNotDetectedWhenToolOnlyListed(t *testing.T) {
	req := &anthropic.Request{
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "search"}}},
		Tools:    []anthropic.Tool{{Name: "web_search", Type: "web_search_20250305"}},
	}
	if IsWebServerToolRequest(req) {
		t.Fatal("listed-only web_search should not trigger local handler")
	}
}

func TestWebServerToolDetectedWhenForcedAndListed(t *testing.T) {
	req := &anthropic.Request{
		Messages:   []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "query: relaycode"}}},
		Tools:      []anthropic.Tool{{Name: "web_search", Type: "web_search_20250305"}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"web_search"}`),
	}
	if !IsWebServerToolRequest(req) {
		t.Fatal("forced listed web_search should trigger local handler")
	}
}

func TestWebServerToolNotDetectedWhenForcedNameMissing(t *testing.T) {
	req := &anthropic.Request{
		Tools:      []anthropic.Tool{{Name: "other"}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"web_fetch"}`),
	}
	if IsWebServerToolRequest(req) {
		t.Fatal("forced missing web_fetch should not trigger local handler")
	}
}

func TestForcedToolTurnTextUsesLatestUser(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "old https://old.example.com"}},
		{Role: "assistant", Content: anthropic.Content{Raw: "ok"}},
		{Role: "user", Content: anthropic.Content{Raw: "new https://new.example.com"}},
	}}
	if got := ForcedToolTurnText(req); got != "new https://new.example.com" {
		t.Fatalf("ForcedToolTurnText = %q", got)
	}
}
