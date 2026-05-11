// Package anthropic holds types and helpers for the Anthropic Messages API
// shape that Claude Code speaks.
package anthropic

import (
	"encoding/json"
	"fmt"
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
	Metadata      json.RawMessage `json:"metadata,omitempty"`
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
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// SystemText collapses the Anthropic-style system field into a single string.
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
	for i, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if i > 0 {
			out += "\n\n"
		}
		out += b.Text
	}
	return out, nil
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
