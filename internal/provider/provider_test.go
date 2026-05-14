package provider

import "testing"

func TestInstructionsWithToolUseBridge(t *testing.T) {
	if got := InstructionsWithToolUseBridge("base", false); got != "base" {
		t.Fatalf("no tools = %q", got)
	}
	if got := InstructionsWithToolUseBridge("", true); got != ToolUseBridgeInstruction {
		t.Fatalf("empty with tools = %q", got)
	}
	got := InstructionsWithToolUseBridge("base", true)
	want := "base\n\n" + ToolUseBridgeInstruction
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
