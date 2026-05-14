// Package config loads the YAML config used by RelayCode.
//
// Intentionally zero-dep: a minimal YAML subset parser lives in yaml.go.
// Supported: nested string-keyed maps, lists of maps, scalar string/number/bool
// values, "${ENV}" expansion. No anchors, no multi-line strings, no flow style.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Server    ServerConfig
	Routes    []Route
	Providers map[string]ProviderConfig
}

type ServerConfig struct {
	Host                         string
	Port                         int
	AuthToken                    string
	EnableWebServerTools         bool
	WebFetchAllowedSchemes       string
	WebFetchAllowPrivateNetworks bool
	FastPrefixDetection          bool
	EnableNetworkProbeMock       bool
	EnableTitleGenerationSkip    bool
	EnableSuggestionModeSkip     bool
	EnableFilepathExtractionMock bool
	LogRequestSnapshots          bool
	CompactToolResults           bool
	ResponsesSessionStorePath    string
}

type Route struct {
	Match    string // substring matched against incoming model name; "*" = fallback
	Provider string // key into Config.Providers
	Model    string // upstream model id to send
}

type ProviderKind string

const (
	KindOpenAIChat        ProviderKind = "openai_chat"
	KindOpenAIResponses   ProviderKind = "openai_responses"
	KindAnthropicMessages ProviderKind = "anthropic_messages"
)

type ProviderConfig struct {
	Kind                               ProviderKind
	BaseURL                            string
	APIKey                             string
	ExperimentalPassthroughServerTools bool
	CodexAuthPath                      string
	HTTPTimeoutSeconds                 int
	HTTPProxy                          string
	MaxRetries                         int
	MaxConcurrency                     int
	ExperimentalPreviousResponseID     bool
	CompactToolResults                 bool
}

// Load reads the YAML config at path and validates it.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := parseYAML(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg, err := fromDoc(doc)
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func fromDoc(doc yamlMap) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Host:                         "127.0.0.1",
			Port:                         8080,
			WebFetchAllowedSchemes:       "http,https",
			FastPrefixDetection:          true,
			EnableNetworkProbeMock:       true,
			EnableTitleGenerationSkip:    true,
			EnableSuggestionModeSkip:     true,
			EnableFilepathExtractionMock: true,
		},
		Providers: map[string]ProviderConfig{},
	}

	if srv, ok := doc["server"].(yamlMap); ok {
		if v, ok := srv["host"].(string); ok && v != "" {
			cfg.Server.Host = v
		}
		if v, ok := srv["port"].(int); ok {
			cfg.Server.Port = v
		}
		if v, ok := srv["auth_token"].(string); ok {
			cfg.Server.AuthToken = expandEnv(v)
		}
		cfg.Server.EnableWebServerTools = boolAt(srv, "enable_web_server_tools")
		if v, ok := srv["web_fetch_allowed_schemes"].(string); ok && strings.TrimSpace(v) != "" {
			cfg.Server.WebFetchAllowedSchemes = v
		}
		cfg.Server.WebFetchAllowPrivateNetworks = boolAt(srv, "web_fetch_allow_private_networks")
		cfg.Server.FastPrefixDetection = boolDefault(srv, "fast_prefix_detection", cfg.Server.FastPrefixDetection)
		cfg.Server.EnableNetworkProbeMock = boolDefault(srv, "enable_network_probe_mock", cfg.Server.EnableNetworkProbeMock)
		cfg.Server.EnableTitleGenerationSkip = boolDefault(srv, "enable_title_generation_skip", cfg.Server.EnableTitleGenerationSkip)
		cfg.Server.EnableSuggestionModeSkip = boolDefault(srv, "enable_suggestion_mode_skip", cfg.Server.EnableSuggestionModeSkip)
		cfg.Server.EnableFilepathExtractionMock = boolDefault(srv, "enable_filepath_extraction_mock", cfg.Server.EnableFilepathExtractionMock)
		cfg.Server.LogRequestSnapshots = boolAt(srv, "log_request_snapshots")
		cfg.Server.CompactToolResults = boolAt(srv, "compact_tool_results")
		if v, ok := srv["responses_session_store_path"].(string); ok {
			cfg.Server.ResponsesSessionStorePath = expandEnv(v)
		}
	}

	routes, ok := doc["routes"].(yamlList)
	if !ok {
		return nil, errors.New("missing 'routes'")
	}
	for i, r := range routes {
		entry, ok := r.(yamlMap)
		if !ok {
			return nil, fmt.Errorf("routes[%d]: expected a map", i)
		}
		rt := Route{
			Match:    stringAt(entry, "match"),
			Provider: stringAt(entry, "provider"),
			Model:    stringAt(entry, "model"),
		}
		if rt.Match == "" || rt.Provider == "" || rt.Model == "" {
			return nil, fmt.Errorf("routes[%d]: match, provider, and model are required", i)
		}
		cfg.Routes = append(cfg.Routes, rt)
	}

	providers, ok := doc["providers"].(yamlMap)
	if !ok {
		return nil, errors.New("missing 'providers'")
	}
	for name, raw := range providers {
		entry, ok := raw.(yamlMap)
		if !ok {
			return nil, fmt.Errorf("providers.%s: expected a map", name)
		}
		pc := ProviderConfig{
			Kind:                               ProviderKind(stringAt(entry, "kind")),
			BaseURL:                            strings.TrimRight(expandEnv(stringAt(entry, "base_url")), "/"),
			APIKey:                             expandEnv(stringAt(entry, "api_key")),
			ExperimentalPassthroughServerTools: boolAt(entry, "experimental_passthrough_server_tools"),
			CodexAuthPath:                      expandEnv(stringAt(entry, "codex_auth_path")),
			HTTPTimeoutSeconds:                 intAt(entry, "http_timeout_seconds"),
			HTTPProxy:                          expandEnv(stringAt(entry, "http_proxy")),
			MaxRetries:                         intAt(entry, "max_retries"),
			MaxConcurrency:                     intAt(entry, "max_concurrency"),
			ExperimentalPreviousResponseID:     boolAt(entry, "experimental_previous_response_id"),
		}
		if pc.Kind != KindOpenAIChat && pc.Kind != KindOpenAIResponses && pc.Kind != KindAnthropicMessages {
			return nil, fmt.Errorf("providers.%s: unknown kind %q (want openai_chat|openai_responses|anthropic_messages)", name, pc.Kind)
		}
		if pc.BaseURL == "" {
			return nil, fmt.Errorf("providers.%s: base_url required", name)
		}
		cfg.Providers[name] = pc
	}

	return cfg, nil
}

func (c *Config) validate() error {
	hasFallback := false
	for _, r := range c.Routes {
		if r.Match == "*" {
			hasFallback = true
		}
		if _, ok := c.Providers[r.Provider]; !ok {
			return fmt.Errorf("route match=%q references unknown provider %q", r.Match, r.Provider)
		}
	}
	if !hasFallback {
		return errors.New(`routes: a fallback route with match: "*" is required`)
	}
	return nil
}

func stringAt(m yamlMap, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolAt(m yamlMap, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func boolDefault(m yamlMap, key string, fallback bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return fallback
}

func intAt(m yamlMap, key string) int {
	if v, ok := m[key].(int); ok {
		return v
	}
	return 0
}

// expandEnv replaces ${NAME} segments with os.Getenv("NAME"). Missing -> "".
func expandEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end >= 0 {
				b.WriteString(os.Getenv(s[i+2 : i+2+end]))
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
