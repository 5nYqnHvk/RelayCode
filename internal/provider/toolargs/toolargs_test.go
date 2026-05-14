package toolargs

import (
	"encoding/json"
	"testing"
)

func TestSanitizeParametersAliasesTypeAndRequired(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"type":{"type":"string"},
			"name":{"type":"string"}
		},
		"required":["type","name"]
	}`)

	out, aliases := SanitizeParameters(raw)
	if aliases["_fcc_arg_type"] != "type" {
		t.Fatalf("aliases = %#v", aliases)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["type"]; ok {
		t.Fatalf("type property still present: %#v", schema.Properties)
	}
	if _, ok := schema.Properties["_fcc_arg_type"]; !ok {
		t.Fatalf("alias property missing: %#v", schema.Properties)
	}
	if got := schema.Required[0]; got != "_fcc_arg_type" {
		t.Fatalf("required[0] = %q", got)
	}
}

func TestSanitizeParametersAvoidsAliasCollision(t *testing.T) {
	raw := json.RawMessage(`{
		"properties":{
			"type":{"type":"string"},
			"_fcc_arg_type":{"type":"string"}
		},
		"required":["type"]
	}`)

	out, aliases := SanitizeParameters(raw)
	if aliases["_fcc_arg_type_2"] != "type" {
		t.Fatalf("aliases = %#v", aliases)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["_fcc_arg_type"]; !ok {
		t.Fatalf("original collision key missing: %#v", schema.Properties)
	}
	if _, ok := schema.Properties["_fcc_arg_type_2"]; !ok {
		t.Fatalf("second alias key missing: %#v", schema.Properties)
	}
	if got := schema.Required[0]; got != "_fcc_arg_type_2" {
		t.Fatalf("required[0] = %q", got)
	}
}

func TestSanitizeParametersPassesThroughInvalidOrUnchangedSchema(t *testing.T) {
	invalid := json.RawMessage(`{`)
	out, aliases := SanitizeParameters(invalid)
	if string(out) != string(invalid) || aliases != nil {
		t.Fatalf("invalid out=%q aliases=%#v", out, aliases)
	}

	plain := json.RawMessage(`{"properties":{"name":{"type":"string"}}}`)
	out, aliases = SanitizeParameters(plain)
	if string(out) != string(plain) || aliases != nil {
		t.Fatalf("plain out=%q aliases=%#v", out, aliases)
	}
}

func TestRestoreArgsBuffersUntilCompleteThenRestoresNestedAliases(t *testing.T) {
	aliases := map[string]map[string]string{
		"tool": {"_fcc_arg_type": "type"},
	}
	buffers := map[int]string{}

	out, ok := RestoreArgs(7, "tool", `{"items":[{"_fcc_arg_type":"`, aliases, buffers)
	if ok || out != "" {
		t.Fatalf("partial out=%q ok=%v", out, ok)
	}
	if buffers[7] == "" {
		t.Fatal("partial args not buffered")
	}

	out, ok = RestoreArgs(7, "tool", `book"}],"outer":{"_fcc_arg_type":"box"}}`, aliases, buffers)
	if !ok {
		t.Fatal("complete args not restored")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("restored invalid JSON %q: %v", out, err)
	}
	items := got["items"].([]any)
	item := items[0].(map[string]any)
	if item["type"] != "book" {
		t.Fatalf("item alias not restored: %#v", item)
	}
	outer := got["outer"].(map[string]any)
	if outer["type"] != "box" {
		t.Fatalf("outer alias not restored: %#v", outer)
	}
	if _, exists := buffers[7]; exists {
		t.Fatalf("buffer not cleared: %#v", buffers)
	}
}

func TestRestoreArgsNoAliasReturnsInput(t *testing.T) {
	out, ok := RestoreArgs(0, "other", `{bad`, map[string]map[string]string{"tool": {"x": "y"}}, map[int]string{})
	if !ok || out != `{bad` {
		t.Fatalf("out=%q ok=%v", out, ok)
	}
}

func TestRestoreCompleteArgsFallsBackOnIncomplete(t *testing.T) {
	aliases := map[string]map[string]string{"tool": {"_fcc_arg_type": "type"}}
	if got := RestoreCompleteArgs("tool", `{"_fcc_arg_type":`, aliases); got != `{"_fcc_arg_type":` {
		t.Fatalf("got %q", got)
	}
	if got := RestoreCompleteArgs("tool", `{"_fcc_arg_type":"x"}`, aliases); got != `{"type":"x"}` {
		t.Fatalf("got %q", got)
	}
}
