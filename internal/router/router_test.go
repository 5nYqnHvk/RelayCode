package router

import (
	"testing"

	"github.com/5nYqnHvk/RelayCode/internal/config"
)

func TestResolveFirstCaseInsensitiveSubstringMatch(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{Match: "sonnet", Provider: "fast", Model: "gpt-fast"},
			{Match: "*", Provider: "fallback", Model: "gpt-fallback"},
		},
		Providers: map[string]config.ProviderConfig{
			"fast":     {Kind: config.KindOpenAIResponses, BaseURL: "https://fast.example"},
			"fallback": {Kind: config.KindOpenAIChat, BaseURL: "https://fallback.example"},
		},
	}

	got, err := New(cfg).Resolve("claude-SONNET-4-6")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got.ProviderName != "fast" || got.Model != "gpt-fast" || got.Provider.Kind != config.KindOpenAIResponses {
		t.Fatalf("Resolve() = %+v, want fast gpt-fast openai_responses", got)
	}
}

func TestResolveUsesFallbackWhenNoRouteMatches(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{Match: "opus", Provider: "strong", Model: "gpt-strong"},
			{Match: "*", Provider: "fallback", Model: "gpt-fallback"},
		},
		Providers: map[string]config.ProviderConfig{
			"strong":   {Kind: config.KindOpenAIResponses, BaseURL: "https://strong.example"},
			"fallback": {Kind: config.KindOpenAIChat, BaseURL: "https://fallback.example"},
		},
	}

	got, err := New(cfg).Resolve("claude-haiku-4-5")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got.ProviderName != "fallback" || got.Model != "gpt-fallback" {
		t.Fatalf("Resolve() = %+v, want fallback gpt-fallback", got)
	}
}

func TestResolveErrorsOnUnknownProvider(t *testing.T) {
	cfg := &config.Config{
		Routes:    []config.Route{{Match: "*", Provider: "missing", Model: "gpt"}},
		Providers: map[string]config.ProviderConfig{},
	}

	if _, err := New(cfg).Resolve("anything"); err == nil {
		t.Fatal("Resolve returned nil error for unknown provider")
	}
}
