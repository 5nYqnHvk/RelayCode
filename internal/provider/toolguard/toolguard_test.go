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
	for _, schema := range []string{
		`{"type":"object","properties":{"file_path":{"type":"string","contentEncoding":"base64"}},"required":["file_path"]}`,
		`{"type":"object","propertyNames":{"pattern":"^[a-z]+$"}}`,
		`{"type":"object","patternProperties":{"^x-":{"type":"string"}}}`,
		`{"$ref":"#/$defs/read","$defs":{"read":{"type":"object"}}}`,
	} {
		registry := NewRegistry([]anthropic.Tool{{
			Name:        "Read",
			InputSchema: json.RawMessage(schema),
		}}, false, nil)

		if _, ok := registry.Validate("Read", `{"file_path":"/tmp/x"}`); ok {
			t.Fatalf("unsupported schema keyword validated: %s", schema)
		}
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

func TestRegistryStripsEmptyOptionalArgs(t *testing.T) {
	registry := NewRegistry([]anthropic.Tool{{
		Name: "Read",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string"},
				"offset": {"type": "integer"},
				"pages": {"type": "string"}
			},
			"required": ["file_path"],
			"additionalProperties": false
		}`),
	}}, false, nil)

	restored, ok := registry.Validate("Read", `{"file_path":"/tmp/a","offset":1,"pages":""}`)
	if !ok {
		t.Fatalf("valid args rejected")
	}
	if restored == "" {
		t.Fatal("empty restored")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(restored), &parsed); err != nil {
		t.Fatalf("restored not JSON: %v", err)
	}
	if _, has := parsed["pages"]; has {
		t.Fatalf("empty optional pages not stripped: %s", restored)
	}
	if v, _ := parsed["offset"].(float64); v != 1 {
		t.Fatalf("offset altered: %s", restored)
	}

	restored2, ok := registry.Validate("Read", `{"file_path":"","offset":0}`)
	if !ok {
		t.Fatalf("required empty-string rejected by validator; got=%q", restored2)
	}
	var parsed2 map[string]any
	if err := json.Unmarshal([]byte(restored2), &parsed2); err != nil {
		t.Fatal(err)
	}
	if _, has := parsed2["file_path"]; !has {
		t.Fatalf("required field stripped: %s", restored2)
	}
}
