package anthropic

import "encoding/json"

type RequestSnapshot struct {
	Model        string            `json:"model"`
	MessageCount int               `json:"message_count"`
	Messages     []MessageSnapshot `json:"messages"`
	ToolNames    []string          `json:"tool_names,omitempty"`
	HasSystem    bool              `json:"has_system"`
	HasThinking  bool              `json:"has_thinking"`
}

type MessageSnapshot struct {
	Role          string   `json:"role"`
	ContentKind   string   `json:"content_kind"`
	ContentLength int      `json:"content_length,omitempty"`
	BlockTypes    []string `json:"block_types,omitempty"`
}

func Snapshot(req *Request) RequestSnapshot {
	out := RequestSnapshot{
		Model:        req.Model,
		MessageCount: len(req.Messages),
		HasSystem:    len(req.System) > 0,
		HasThinking:  len(req.Thinking) > 0,
	}
	for _, tool := range req.Tools {
		out.ToolNames = append(out.ToolNames, tool.Name)
	}
	for _, msg := range req.Messages {
		item := MessageSnapshot{Role: msg.Role}
		if msg.Content.Blocks != nil {
			item.ContentKind = "blocks"
			limit := len(msg.Content.Blocks)
			if limit > 12 {
				limit = 12
			}
			for _, block := range msg.Content.Blocks[:limit] {
				item.BlockTypes = append(item.BlockTypes, block.Type)
			}
		} else {
			item.ContentKind = "string"
			item.ContentLength = len(msg.Content.Raw)
		}
		out.Messages = append(out.Messages, item)
	}
	return out
}

func SnapshotJSON(req *Request) string {
	raw, err := json.Marshal(Snapshot(req))
	if err != nil {
		return "{}"
	}
	return string(raw)
}
