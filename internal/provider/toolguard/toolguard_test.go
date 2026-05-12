package toolguard

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
)

func TestRegistryRejectsBadInputSchema(t *testing.T) {
	registry := NewRegistry([]anthropic.Tool{{
		Name:        "Read",
		InputSchema: json.RawMessage(`{"type":"object"`),
	}}, false, nil)

	if _, ok := registry.Validate("Read", `{"file_path":"/tmp/x"}`); ok {
		t.Fatal("bad schema validated")
	}
}

func TestRegistryRejectsUnsupportedSchemaKeyword(t *testing.T) {
	registry := NewRegistry([]anthropic.Tool{{
		Name:        "Read",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string","format":"uri"}},"required":["file_path"]}`),
	}}, false, nil)

	if _, ok := registry.Validate("Read", `{"file_path":"/tmp/x"}`); ok {
		t.Fatal("unsupported schema keyword validated")
	}
}

func TestRegistryValidatesAdditionalPropertiesSchema(t *testing.T) {
	registry := NewRegistry([]anthropic.Tool{{
		Name:        "Meta",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":{"type":"string"}}`),
	}}, false, nil)

	if _, ok := registry.Validate("Meta", `{"ok":"value"}`); !ok {
		t.Fatal("valid additional property rejected")
	}
	if _, ok := registry.Validate("Meta", `{"bad":123}`); ok {
		t.Fatal("invalid additional property validated")
	}
}

func TestRegistryValidatesCommonConstraints(t *testing.T) {
	registry := NewRegistry([]anthropic.Tool{{
		Name:        "Find",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","pattern":"^[a-z]+$","minLength":2},"count":{"type":"integer","minimum":1,"maximum":3},"items":{"type":"array","minItems":1,"items":{"type":"string"}}},"required":["pattern","count","items"]}`),
	}}, false, nil)

	if _, ok := registry.Validate("Find", `{"pattern":"ab","count":2,"items":["x"]}`); !ok {
		t.Fatal("valid constrained args rejected")
	}
	for _, args := range []string{
		`{"pattern":"A","count":2,"items":["x"]}`,
		`{"pattern":"ab","count":4,"items":["x"]}`,
		`{"pattern":"ab","count":2,"items":[]}`,
		`{"pattern":"ab","count":2,"items":[1]}`,
	} {
		if _, ok := registry.Validate("Find", args); ok {
			t.Fatalf("invalid constrained args validated: %s", args)
		}
	}
}
