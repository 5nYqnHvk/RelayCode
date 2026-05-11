package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotOmitsRawContent(t *testing.T) {
	req := &Request{
		Model:    "claude",
		Messages: []Message{{Role: "user", Content: Content{Raw: "secret prompt text"}}, {Role: "assistant", Content: Content{Blocks: []Block{{Type: "tool_use", Name: "Read"}}}}},
		Tools:    []Tool{{Name: "Read"}},
	}
	raw := SnapshotJSON(req)
	if strings.Contains(raw, "secret prompt text") {
		t.Fatalf("snapshot leaked raw content: %s", raw)
	}
	var snap RequestSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Model != "claude" || snap.MessageCount != 2 || snap.Messages[0].ContentLength != len("secret prompt text") || snap.Messages[1].BlockTypes[0] != "tool_use" || snap.ToolNames[0] != "Read" {
		t.Fatalf("snapshot = %+v", snap)
	}
}
