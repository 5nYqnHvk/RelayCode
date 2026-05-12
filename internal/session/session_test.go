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
