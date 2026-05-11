package webtools

import (
	"encoding/json"
	"strings"

	"github.com/relaycode/relaycode/internal/anthropic"
)

func ForcedServerToolName(req *anthropic.Request) string {
	if len(req.ToolChoice) == 0 {
		return ""
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.ToolChoice, &choice); err != nil {
		return ""
	}
	if choice.Type != "tool" {
		return ""
	}
	if choice.Name == "web_search" || choice.Name == "web_fetch" {
		return choice.Name
	}
	return ""
}

func HasToolNamed(req *anthropic.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func IsWebServerToolRequest(req *anthropic.Request) bool {
	name := ForcedServerToolName(req)
	return name != "" && HasToolNamed(req, name)
}

func IsAnthropicWebServerTool(tool anthropic.Tool) bool {
	name := strings.TrimSpace(tool.Name)
	if name == "web_search" || name == "web_fetch" {
		return true
	}
	return strings.HasPrefix(tool.Type, "web_search") || strings.HasPrefix(tool.Type, "web_fetch")
}

func HasListedAnthropicWebServerTools(req *anthropic.Request) bool {
	for _, tool := range req.Tools {
		if IsAnthropicWebServerTool(tool) {
			return true
		}
	}
	return false
}

func ForcedToolTurnText(req *anthropic.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return ContentText(req.Messages[i].Content)
		}
	}
	return ""
}
