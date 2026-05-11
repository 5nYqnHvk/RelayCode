package optim

import (
	"encoding/json"
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
)

func TestTryQuotaMock(t *testing.T) {
	req := &anthropic.Request{MaxTokens: 1, Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "quota"}}}}
	resp, ok := Try(req, defaults())
	if !ok || resp.Text != "Quota check passed." {
		t.Fatalf("resp=%+v ok=%v", resp, ok)
	}
}

func TestTryTitleSkip(t *testing.T) {
	req := &anthropic.Request{
		System:   json.RawMessage(`"Return JSON with a title field for this coding session"`),
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "hello"}}},
	}
	resp, ok := Try(req, defaults())
	if !ok || resp.Text != "Conversation" {
		t.Fatalf("resp=%+v ok=%v", resp, ok)
	}
}

func TestTryPrefixDetection(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "<policy_spec>x</policy_spec>\nCommand: git commit -m hi"}}}}
	resp, ok := Try(req, defaults())
	if !ok || resp.Text != "git commit" {
		t.Fatalf("resp=%+v ok=%v", resp, ok)
	}
}

func TestTrySuggestionSkip(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "[SUGGESTION MODE: next]"}}}}
	resp, ok := Try(req, defaults())
	if !ok || resp.Text != "" {
		t.Fatalf("resp=%+v ok=%v", resp, ok)
	}
}

func TestTryFilepathMock(t *testing.T) {
	req := &anthropic.Request{Messages: []anthropic.Message{{Role: "user", Content: anthropic.Content{Raw: "Extract filepaths\nCommand: cat internal/server/server.go\nOutput: package server"}}}}
	resp, ok := Try(req, defaults())
	if !ok || resp.Text != "<filepaths>\ninternal/server/server.go\n</filepaths>" {
		t.Fatalf("resp=%+v ok=%v", resp, ok)
	}
}

func defaults() config.ServerConfig {
	return config.ServerConfig{
		FastPrefixDetection:          true,
		EnableNetworkProbeMock:       true,
		EnableTitleGenerationSkip:    true,
		EnableSuggestionModeSkip:     true,
		EnableFilepathExtractionMock: true,
	}
}
