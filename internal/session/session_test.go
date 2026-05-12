package session

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
)

func TestHashToolsIncludesTypeAndStrict(t *testing.T) {
	strictFalse := false
	strictTrue := true
	base := []anthropic.Tool{{Name: "web_search", Type: "custom", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strictFalse}}
	typeChanged := []anthropic.Tool{{Name: "web_search", Type: "web_search_20250305", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strictFalse}}
	strictChanged := []anthropic.Tool{{Name: "web_search", Type: "custom", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strictTrue}}

	h1, err := hashTools(base)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashTools(typeChanged)
	if err != nil {
		t.Fatal(err)
	}
	h3, err := hashTools(strictChanged)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatalf("tool type change did not change hash: %s", h1)
	}
	if h1 == h3 {
		t.Fatalf("tool strict change did not change hash: %s", h1)
	}
}

func TestPrepareChainsAppendedLastUserBlocks(t *testing.T) {
	store := NewStore(0, 10)
	first := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "first"}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "ready"}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "old"}}}},
	}}
	lookup, err := store.Prepare("provider", "model", first)
	if err != nil {
		t.Fatal(err)
	}
	store.Commit(lookup, "provider", "model", len(first.Messages), "resp_1", 1, 1)

	second := &anthropic.Request{Messages: []anthropic.Message{
		{Role: "user", Content: anthropic.Content{Raw: "first"}},
		{Role: "assistant", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "ready"}}}},
		{Role: "user", Content: anthropic.Content{Blocks: []anthropic.Block{{Type: "text", Text: "old"}, {Type: "text", Text: "new"}}}},
	}}
	lookup, err = store.Prepare("provider", "model", second)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Chain == nil || lookup.Chain.ResponseID != "resp_1" {
		t.Fatalf("chain = %+v reason=%q", lookup.Chain, lookup.FullReplayReason)
	}
	if len(lookup.Tail) != 1 {
		t.Fatalf("tail = %+v", lookup.Tail)
	}
	blocks := lookup.Tail[0].Content.AsBlocks()
	if lookup.Tail[0].Role != "user" || len(blocks) != 1 || blocks[0].Text != "new" {
		t.Fatalf("tail = %+v", lookup.Tail)
	}
}
