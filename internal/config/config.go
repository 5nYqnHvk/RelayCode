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
	Server         ServerConfig
	Routes         []Route
	Providers      map[string]ProviderConfig
	ToolValidation ToolValidationConfig
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
	EnableUpdateNotification     bool
	UpdateCheckURL               string
	UpdateCheckTimeoutSeconds    int
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
	ResponsesCustomToolMode            string
	ResponsesNamespaceTools            bool
	CompactToolResults                 bool
	ServiceTier                        string
	ResponsesReasoningSummary          string
	ResponsesParallelToolCalls         *bool
	ToolValidation                     ToolValidationConfig
}

type ToolValidationConfig struct {
	UnknownTools       string
	InvalidKnownTools  string
	MalformedArguments string
}

func lookupPath(m yamlMap, path ...string) (any, bool) {
	cur := any(m)
	for _, key := range path {
		mm, ok := cur.(yamlMap)
		if !ok {
			return nil, false
		}
		cur, ok = mm[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func mapAt(m yamlMap, path ...string) (yamlMap, bool) {
	v, ok := lookupPath(m, path...)
	if !ok {
		return nil, false
	}
	mm, ok := v.(yamlMap)
	if !ok {
		return nil, false
	}
	return mm, true
}

func stringAtPath(m yamlMap, path ...string) (string, bool) {
	v, ok := lookupPath(m, path...)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func boolAtPath(m yamlMap, path ...string) (bool, bool) {
	v, ok := lookupPath(m, path...)
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	if !ok {
		return false, false
	}
	return b, true
}

func intAtPath(m yamlMap, path ...string) (int, bool) {
	v, ok := lookupPath(m, path...)
	if !ok {
		return 0, false
	}
	n, ok := v.(int)
	if !ok {
		return 0, false
	}
	return n, true
}

func boolPtrAtPath(m yamlMap, path ...string) (*bool, bool) {
	v, ok := lookupPath(m, path...)
	if !ok {
		return nil, false
	}
	b, ok := v.(bool)
	if !ok {
		return nil, false
	}
	return &b, true
}

func stringAtAny(m yamlMap, nested []string, flat string) string {
	if v, ok := stringAtPath(m, nested...); ok {
		return v
	}
	if v, ok := m[flat].(string); ok {
		return v
	}
	return ""
}

func boolAtAny(m yamlMap, nested []string, flat string) bool {
	if v, ok := boolAtPath(m, nested...); ok {
		return v
	}
	if v, ok := m[flat].(bool); ok {
		return v
	}
	return false
}

func boolDefaultAny(m yamlMap, fallback bool, nested []string, flat string) bool {
	if v, ok := boolAtPath(m, nested...); ok {
		return v
	}
	if v, ok := m[flat].(bool); ok {
		return v
	}
	return fallback
}

func intAtAny(m yamlMap, nested []string, flat string) int {
	if v, ok := intAtPath(m, nested...); ok {
		return v
	}
	if v, ok := m[flat].(int); ok {
		return v
	}
	return 0
}

func boolPtrAtAny(m yamlMap, nested []string, flat string) *bool {
	if v, ok := boolPtrAtPath(m, nested...); ok {
		return v
	}
	if v, ok := m[flat].(bool); ok {
		return &v
	}
	return nil
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
			UpdateCheckURL:               "https://api.github.com/repos/5nYqnHvk/RelayCode/releases/latest",
			UpdateCheckTimeoutSeconds:    3,
			EnableTitleGenerationSkip:    true,
			EnableSuggestionModeSkip:     true,
			EnableFilepathExtractionMock: true,
		},
		Providers: map[string]ProviderConfig{},
		ToolValidation: ToolValidationConfig{
			UnknownTools:       "drop",
			InvalidKnownTools:  "warn",
			MalformedArguments: "repair",
		},
	}

	if srv, ok := doc["server"].(yamlMap); ok {
		cfg.Server.Host = stringAtAny(srv, []string{"network", "host"}, "host")
		if cfg.Server.Host == "" {
			cfg.Server.Host = "127.0.0.1"
		}
		if v := intAtAny(srv, []string{"network", "port"}, "port"); v != 0 {
			cfg.Server.Port = v
		}
		if v := stringAtAny(srv, []string{"network", "auth_token"}, "auth_token"); v != "" {
			cfg.Server.AuthToken = expandEnv(v)
		}
		cfg.Server.EnableWebServerTools = boolAtAny(srv, []string{"web_tools", "enable"}, "enable_web_server_tools")
		if v := stringAtAny(srv, []string{"web_tools", "allowed_schemes"}, "web_fetch_allowed_schemes"); strings.TrimSpace(v) != "" {
			cfg.Server.WebFetchAllowedSchemes = v
		}
		cfg.Server.WebFetchAllowPrivateNetworks = boolAtAny(srv, []string{"web_tools", "allow_private_networks"}, "web_fetch_allow_private_networks")
		cfg.Server.FastPrefixDetection = boolDefaultAny(srv, cfg.Server.FastPrefixDetection, []string{"claude_code", "fast_prefix_detection"}, "fast_prefix_detection")
		cfg.Server.EnableNetworkProbeMock = boolDefaultAny(srv, cfg.Server.EnableNetworkProbeMock, []string{"claude_code", "enable_network_probe_mock"}, "enable_network_probe_mock")
		cfg.Server.EnableTitleGenerationSkip = boolDefaultAny(srv, cfg.Server.EnableTitleGenerationSkip, []string{"claude_code", "enable_title_generation_skip"}, "enable_title_generation_skip")
		cfg.Server.EnableSuggestionModeSkip = boolDefaultAny(srv, cfg.Server.EnableSuggestionModeSkip, []string{"claude_code", "enable_suggestion_mode_skip"}, "enable_suggestion_mode_skip")
		cfg.Server.EnableFilepathExtractionMock = boolDefaultAny(srv, cfg.Server.EnableFilepathExtractionMock, []string{"claude_code", "enable_filepath_extraction_mock"}, "enable_filepath_extraction_mock")
		cfg.Server.LogRequestSnapshots = boolAtAny(srv, []string{"logging", "log_request_snapshots"}, "log_request_snapshots")
		cfg.Server.CompactToolResults = boolAtAny(srv, []string{"logging", "compact_tool_results"}, "compact_tool_results")
		cfg.Server.EnableUpdateNotification = boolAtAny(srv, []string{"updates", "enable_notification"}, "enable_update_notification")
		if v := stringAtAny(srv, []string{"updates", "check_url"}, "update_check_url"); strings.TrimSpace(v) != "" {
			cfg.Server.UpdateCheckURL = expandEnv(v)
		}
		if v := intAtAny(srv, []string{"updates", "check_timeout_seconds"}, "update_check_timeout_seconds"); v > 0 {
			cfg.Server.UpdateCheckTimeoutSeconds = v
		}
		if v := stringAtAny(srv, []string{"responses", "session_store_path"}, "responses_session_store_path"); v != "" {
			cfg.Server.ResponsesSessionStorePath = expandEnv(v)
		}
	}

	if tv, ok := doc["tool_validation"].(yamlMap); ok {
		if v := strings.TrimSpace(stringAt(tv, "unknown_tools")); v != "" {
			cfg.ToolValidation.UnknownTools = v
		}
		if v := strings.TrimSpace(stringAt(tv, "invalid_known_tools")); v != "" {
			cfg.ToolValidation.InvalidKnownTools = v
		}
		if v := strings.TrimSpace(stringAt(tv, "malformed_arguments")); v != "" {
			cfg.ToolValidation.MalformedArguments = v
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
			BaseURL:                            strings.TrimRight(expandEnv(stringAtAny(entry, []string{"endpoint", "base_url"}, "base_url")), "/"),
			APIKey:                             expandEnv(stringAtAny(entry, []string{"endpoint", "api_key"}, "api_key")),
			ExperimentalPassthroughServerTools: boolAtAny(entry, []string{"experimental", "passthrough_server_tools"}, "experimental_passthrough_server_tools"),
			CodexAuthPath:                      expandEnv(stringAtAny(entry, []string{"endpoint", "codex_auth_path"}, "codex_auth_path")),
			HTTPTimeoutSeconds:                 intAtAny(entry, []string{"http", "timeout_seconds"}, "http_timeout_seconds"),
			HTTPProxy:                          expandEnv(stringAtAny(entry, []string{"http", "proxy"}, "http_proxy")),
			MaxRetries:                         intAtAny(entry, []string{"http", "max_retries"}, "max_retries"),
			MaxConcurrency:                     intAtAny(entry, []string{"http", "max_concurrency"}, "max_concurrency"),
			ExperimentalPreviousResponseID:     boolAtAny(entry, []string{"experimental", "previous_response_id"}, "experimental_previous_response_id"),
			ResponsesCustomToolMode:            strings.TrimSpace(stringAtAny(entry, []string{"responses", "custom_tool_mode"}, "responses_custom_tool_mode")),
			ResponsesNamespaceTools:            boolAtAny(entry, []string{"responses", "namespace_tools"}, "responses_namespace_tools"),
			ServiceTier:                        strings.TrimSpace(stringAtAny(entry, []string{"responses", "service_tier"}, "service_tier")),
			ResponsesReasoningSummary:          strings.TrimSpace(stringAtAny(entry, []string{"responses", "reasoning_summary"}, "responses_reasoning_summary")),
			ResponsesParallelToolCalls:         boolPtrAtAny(entry, []string{"responses", "parallel_tool_calls"}, "responses_parallel_tool_calls"),
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
	if err := validateToolValidation(c.ToolValidation); err != nil {
		return err
	}
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
	for name, provider := range c.Providers {
		if provider.ResponsesCustomToolMode != "" && provider.ResponsesCustomToolMode != "native" {
			if provider.Kind != KindOpenAIResponses {
				return fmt.Errorf("providers.%s: responses_custom_tool_mode is only valid for openai_responses providers", name)
			}
			if provider.ResponsesCustomToolMode != "function" {
				return fmt.Errorf("providers.%s: responses_custom_tool_mode must be native or function", name)
			}
		}
		if provider.ResponsesNamespaceTools && provider.Kind != KindOpenAIResponses {
			return fmt.Errorf("providers.%s: responses_namespace_tools is only valid for openai_responses providers", name)
		}
		if provider.ServiceTier != "" && provider.Kind != KindOpenAIResponses {
			return fmt.Errorf("providers.%s: service_tier is only valid for openai_responses providers", name)
		}
		if provider.ResponsesReasoningSummary != "" && provider.Kind != KindOpenAIResponses {
			return fmt.Errorf("providers.%s: responses_reasoning_summary is only valid for openai_responses providers", name)
		}
		if provider.ResponsesParallelToolCalls != nil && provider.Kind != KindOpenAIResponses {
			return fmt.Errorf("providers.%s: responses_parallel_tool_calls is only valid for openai_responses providers", name)
		}
	}
	return nil
}

func validateToolValidation(tv ToolValidationConfig) error {
	for key, value := range map[string]string{
		"unknown_tools":       tv.UnknownTools,
		"invalid_known_tools": tv.InvalidKnownTools,
		"malformed_arguments": tv.MalformedArguments,
	} {
		switch value {
		case "drop", "warn", "repair":
		default:
			return fmt.Errorf("tool_validation.%s must be drop, warn, or repair", key)
		}
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

func boolPtr(m yamlMap, key string) *bool {
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
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
