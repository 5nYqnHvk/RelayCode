// Package anthropic holds types and helpers for the Anthropic Messages API
// shape that Claude Code speaks.
package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Request is the incoming /v1/messages body.
type Request struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	System        json.RawMessage `json:"system,omitempty"`
	Messages      []Message       `json:"messages"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      json.RawMessage `json:"thinking,omitempty"`
	OutputConfig  *OutputConfig   `json:"output_config,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

// OutputConfig carries Anthropic output configuration fields.
type OutputConfig struct {
	Effort any `json:"effort,omitempty"`
}

func (r *Request) ReasoningEffort() (string, bool) {
	if r == nil || r.OutputConfig == nil || r.OutputConfig.Effort == nil {
		return "", false
	}
	switch v := r.OutputConfig.Effort.(type) {
	case string:
		switch strings.ToLower(v) {
		case "low", "medium", "high":
			return strings.ToLower(v), true
		case "max", "xhigh":
			return "xhigh", true
		case "none", "minimal":
			return strings.ToLower(v), true
		}
	case float64:
		if v <= 50 {
			return "low", true
		}
		if v <= 85 {
			return "medium", true
		}
		if v <= 100 {
			return "high", true
		}
		return "xhigh", true
	case int:
		if v <= 50 {
			return "low", true
		}
		if v <= 85 {
			return "medium", true
		}
		if v <= 100 {
			return "high", true
		}
		return "xhigh", true
	}
	return "", false
}

// Message is one turn in the conversation.
type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// Content is either a plain string or a list of typed blocks.
type Content struct {
	Raw    string
	Blocks []Block
}

func (c *Content) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] == '"' {
		return json.Unmarshal(b, &c.Raw)
	}
	return json.Unmarshal(b, &c.Blocks)
}

func (c Content) MarshalJSON() ([]byte, error) {
	if c.Blocks != nil {
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Raw)
}

// Blocks returns the content normalized to a block list.
func (c Content) AsBlocks() []Block {
	if c.Blocks != nil {
		return c.Blocks
	}
	if c.Raw == "" {
		return nil
	}
	return []Block{{Type: "text", Text: c.Raw}}
}

// Block is a content block; fields relevant to its Type are populated.
type Block struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Tool is one function tool exposed to the model.
type Tool struct {
	// Type distinguishes regular function tools from Anthropic server tools
	// like "web_search_20250305" or "web_fetch_20250826". When Type is empty
	// or "custom" the tool is a regular function the upstream model is
	// expected to call; non-empty server-tool Type values indicate Anthropic
	// would execute the tool itself, and the upstream (OpenAI) does not know
	// how to handle them.
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// IsServerTool reports whether this tool is an Anthropic-managed server tool
// (web_search / web_fetch / code execution / etc). These must be stripped
// from requests forwarded to non-Anthropic backends.
func (t Tool) IsServerTool() bool {
	return isServerToolType(t.Type) || isServerToolName(t.Name)
}

func isServerToolType(typ string) bool {
	if typ == "" || typ == "custom" || typ == "function" {
		return false
	}
	// Any tool type that doesn't round-trip as plain "function" is a
	// provider-side tool the upstream OpenAI endpoint cannot execute.
	return true
}

func isServerToolName(name string) bool {
	switch name {
	case "web_search", "web_fetch":
		return true
	}
	return false
}

// FilterClientTools returns the subset of tools the upstream backend can
// actually execute (i.e. everything that is not an Anthropic server tool).
func FilterClientTools(tools []Tool) []Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if t.IsServerTool() {
			continue
		}
		out = append(out, t)
	}
	return out
}

func ToolsForUpstream(tools []Tool, passthroughServerTools bool) []Tool {
	if passthroughServerTools {
		return tools
	}
	return FilterClientTools(tools)
}

// IsServerToolBlock reports whether a message block describes an Anthropic
// server-side tool interaction that the upstream cannot interpret.
func IsServerToolBlock(b Block) bool {
	switch b.Type {
	case "server_tool_use",
		"web_search_tool_result",
		"web_fetch_tool_result",
		"code_execution_tool_result",
		"computer_use_tool_result",
		"mcp_tool_use",
		"mcp_tool_result":
		return true
	}
	return false
}

// StripServerToolBlocks returns a copy of blocks with any
// server-tool-related block removed.
func StripServerToolBlocks(blocks []Block) []Block {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		if IsServerToolBlock(b) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func BlocksForUpstream(blocks []Block, passthroughServerTools bool) []Block {
	if passthroughServerTools {
		return blocks
	}
	return StripServerToolBlocks(blocks)
}

// SystemText collapses the Anthropic-style system field into a single string.
// Volatile Claude-Code-only header blocks (e.g. the per-turn
// x-anthropic-billing-header line with a rotating session hash) are stripped
// so the instructions prefix stays stable across turns of the same
// conversation and upstream prompt caches can hit.
func SystemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var blocks []Block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("system: %w", err)
	}
	var out string
	first := true
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if isVolatileSystemBlock(b.Text) {
			continue
		}
		if !first {
			out += "\n\n"
		}
		out += b.Text
		first = false
	}
	return out, nil
}

// isVolatileSystemBlock returns true for ingress header lines that change
// every turn and therefore poison the upstream prompt cache. Currently
// detects Claude Code's `x-anthropic-billing-header: ...` preamble which
// carries a rotating session-hash field (`cch=...`).
func isVolatileSystemBlock(text string) bool {
	trimmed := text
	if len(trimmed) > 256 {
		trimmed = trimmed[:256]
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "x-anthropic-billing-header:") {
		return true
	}
	return false
}

// SessionID returns a stable conversation identifier extracted from the
// request metadata. Claude Code sends metadata.user_id as a JSON string
// containing device_id / account_uuid / session_id; session_id is what
// stays constant across turns of the same conversation. Returns "" when
// no usable id is present.
func (r *Request) SessionID() string {
	if len(r.Metadata) == 0 {
		return ""
	}
	var md struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(r.Metadata, &md); err != nil {
		return ""
	}
	if md.UserID == "" {
		return ""
	}
	var inner struct {
		SessionID string `json:"session_id"`
		DeviceID  string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(md.UserID), &inner); err != nil {
		// user_id is a plain string: use it verbatim.
		return md.UserID
	}
	if inner.SessionID != "" {
		return inner.SessionID
	}
	if inner.DeviceID != "" {
		return inner.DeviceID
	}
	return md.UserID
}

// ToolResultText flattens a tool_result block's content field into a string.
func ToolResultText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if raw[0] == '[' {
		var blocks []Block
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return "", err
		}
		var out string
		for i, b := range blocks {
			if b.Type != "text" {
				continue
			}
			if i > 0 {
				out += "\n"
			}
			out += b.Text
		}
		return out, nil
	}
	// object or other scalar — round-trip as JSON text
	return string(raw), nil
}
