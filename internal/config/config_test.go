package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesConfigAndExpandsEnv(t *testing.T) {
	t.Setenv("TEST_RELAYCODE_TOKEN", "secret-token")
	t.Setenv("TEST_RELAYCODE_KEY", "secret-key")

	path := filepath.Join(t.TempDir(), "relaycode.yaml")
	body := `server:
  host: 0.0.0.0
  port: 9090
  auth_token: "${TEST_RELAYCODE_TOKEN}"
  enable_web_server_tools: true
  web_fetch_allowed_schemes: https
  web_fetch_allow_private_networks: true

routes:
  - match: "opus"
    provider: openai_responses
    model: gpt-strong
  - match: "*"
    provider: openai_chat
    model: gpt-fallback

providers:
  openai_responses:
    kind: openai_responses
    base_url: https://api.example.com/v1/
    api_key: "${TEST_RELAYCODE_KEY}"
    experimental_passthrough_server_tools: true
    experimental_previous_response_id: true
    codex_auth_path: /tmp/codex-auth.json
  openai_chat:
    kind: openai_chat
    base_url: https://chat.example.com/v1
    api_key: static-key
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" || cfg.Server.Port != 9090 || cfg.Server.AuthToken != "secret-token" ||
		!cfg.Server.EnableWebServerTools ||
		cfg.Server.WebFetchAllowedSchemes != "https" ||
		!cfg.Server.WebFetchAllowPrivateNetworks {
		t.Fatalf("Server = %+v", cfg.Server)
	}
	if len(cfg.Routes) != 2 || cfg.Routes[0].Match != "opus" || cfg.Routes[1].Match != "*" {
		t.Fatalf("Routes = %+v", cfg.Routes)
	}
	provider := cfg.Providers["openai_responses"]
	if provider.Kind != KindOpenAIResponses ||
		provider.BaseURL != "https://api.example.com/v1" ||
		provider.APIKey != "secret-key" ||
		!provider.ExperimentalPassthroughServerTools ||
		!provider.ExperimentalPreviousResponseID ||
		provider.CodexAuthPath != "/tmp/codex-auth.json" {
		t.Fatalf("openai_responses provider = %+v", provider)
	}
	if cfg.Providers["openai_chat"].ExperimentalPassthroughServerTools {
		t.Fatalf("openai_chat provider = %+v, want experimental passthrough disabled by default", cfg.Providers["openai_chat"])
	}
}

func TestLoadRequiresFallbackRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaycode.yaml")
	body := `routes:
  - match: opus
    provider: p
    model: gpt
providers:
  p:
    kind: openai_chat
    base_url: https://api.example.com/v1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load returned nil error without fallback route")
	}
}

func TestLoadRejectsUnknownProviderKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relaycode.yaml")
	body := `routes:
  - match: "*"
    provider: p
    model: gpt
providers:
  p:
    kind: custom
    base_url: https://api.example.com/v1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load returned nil error for unknown provider kind")
	}
}
