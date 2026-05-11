package chat

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/provider/toolargs"
)

func TestBuildRequestAliasesTypeArgument(t *testing.T) {
	req := &anthropic.Request{Tools: []anthropic.Tool{{
		Name:        "NotionLike",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string"},"name":{"type":"string"}},"required":["type"]}`),
	}}}
	body, aliases, err := buildRequestWithAliases(req, "gpt", false)
	if err != nil {
		t.Fatal(err)
	}
	if aliases["NotionLike"]["_fcc_arg_type"] != "type" {
		t.Fatalf("aliases = %+v", aliases)
	}
	tools := body["tools"].([]openaiTool)
	var params map[string]any
	if err := json.Unmarshal(tools[0].Function.Parameters, &params); err != nil {
		t.Fatal(err)
	}
	props := params["properties"].(map[string]any)
	if _, ok := props["type"]; ok {
		t.Fatalf("schema still has type property: %+v", props)
	}
	if _, ok := props["_fcc_arg_type"]; !ok {
		t.Fatalf("schema missing alias property: %+v", props)
	}
}

func TestRestoreToolArgsRestoresAliases(t *testing.T) {
	aliases := map[string]map[string]string{"NotionLike": {"_fcc_arg_type": "type"}}
	buffers := map[int]string{}
	out, ok := toolargs.RestoreArgs(0, "NotionLike", `{"_fcc_arg_type":"page","nested":{"_fcc_arg_type":"child"}}`, aliases, buffers)
	if !ok {
		t.Fatal("restoreToolArgs buffered complete JSON")
	}
	if out != `{"nested":{"type":"child"},"type":"page"}` {
		t.Fatalf("out = %s", out)
	}
}

func TestRestoreToolArgsBuffersPartialJSON(t *testing.T) {
	aliases := map[string]map[string]string{"NotionLike": {"_fcc_arg_type": "type"}}
	buffers := map[int]string{}
	if out, ok := toolargs.RestoreArgs(0, "NotionLike", `{"_fcc_arg`, aliases, buffers); ok || out != "" {
		t.Fatalf("first restore out=%q ok=%v", out, ok)
	}
	out, ok := toolargs.RestoreArgs(0, "NotionLike", `_type":"page"}`, aliases, buffers)
	if !ok || out != `{"type":"page"}` {
		t.Fatalf("second restore out=%q ok=%v", out, ok)
	}
}
