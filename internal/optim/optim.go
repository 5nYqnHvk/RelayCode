package optim

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
)

type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

func Try(req *anthropic.Request, cfg config.ServerConfig) (Response, bool) {
	if cfg.EnableNetworkProbeMock && isQuotaCheck(req) {
		return Response{Text: "Quota check passed.", InputTokens: 10, OutputTokens: 5}, true
	}
	if cfg.FastPrefixDetection {
		if ok, command := isPrefixDetection(req); ok {
			return Response{Text: extractCommandPrefix(command), InputTokens: 100, OutputTokens: 5}, true
		}
	}
	if cfg.EnableTitleGenerationSkip && isTitleGeneration(req) {
		return Response{Text: "Conversation", InputTokens: 100, OutputTokens: 5}, true
	}
	if cfg.EnableSuggestionModeSkip && isSuggestionMode(req) {
		return Response{Text: "", InputTokens: 100, OutputTokens: 1}, true
	}
	if cfg.EnableFilepathExtractionMock {
		if ok, command, output := isFilepathExtraction(req); ok {
			return Response{Text: extractFilepaths(command, output), InputTokens: 100, OutputTokens: 10}, true
		}
	}
	return Response{}, false
}

func isQuotaCheck(req *anthropic.Request) bool {
	return req.MaxTokens == 1 && len(req.Messages) == 1 && req.Messages[0].Role == "user" && strings.Contains(strings.ToLower(contentText(req.Messages[0].Content)), "quota")
}

func isTitleGeneration(req *anthropic.Request) bool {
	if len(req.System) == 0 || len(req.Tools) > 0 {
		return false
	}
	systemText := strings.ToLower(rawText(req.System))
	if !strings.Contains(systemText, "title") {
		return false
	}
	return strings.Contains(systemText, "sentence-case title") || (strings.Contains(systemText, "return json") && strings.Contains(systemText, "field") && (strings.Contains(systemText, "coding session") || strings.Contains(systemText, "this session")))
}

func isPrefixDetection(req *anthropic.Request) (bool, string) {
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		return false, ""
	}
	text := contentText(req.Messages[0].Content)
	if !strings.Contains(text, "<policy_spec>") || !strings.Contains(text, "Command:") {
		return false, ""
	}
	idx := strings.LastIndex(text, "Command:")
	return true, strings.TrimSpace(text[idx+len("Command:"):])
}

func isSuggestionMode(req *anthropic.Request) bool {
	for _, msg := range req.Messages {
		if msg.Role == "user" && strings.Contains(contentText(msg.Content), "[SUGGESTION MODE:") {
			return true
		}
	}
	return false
}

func isFilepathExtraction(req *anthropic.Request) (bool, string, string) {
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || len(req.Tools) > 0 {
		return false, "", ""
	}
	text := contentText(req.Messages[0].Content)
	if !strings.Contains(text, "Command:") || !strings.Contains(text, "Output:") {
		return false, "", ""
	}
	lower := strings.ToLower(text)
	systemText := strings.ToLower(rawText(req.System))
	if !strings.Contains(lower, "filepaths") && !strings.Contains(lower, "<filepaths>") && !strings.Contains(systemText, "extract any file paths") && !strings.Contains(systemText, "file paths that this command") {
		return false, "", ""
	}
	cmdStart := strings.Index(text, "Command:") + len("Command:")
	outputMarker := strings.Index(text[cmdStart:], "Output:")
	if outputMarker < 0 {
		return false, "", ""
	}
	outputStart := cmdStart + outputMarker
	command := strings.TrimSpace(text[cmdStart:outputStart])
	output := strings.TrimSpace(text[outputStart+len("Output:"):])
	for _, marker := range []string{"<", "\n\n"} {
		if idx := strings.Index(output, marker); idx >= 0 {
			output = strings.TrimSpace(output[:idx])
		}
	}
	return true, command, output
}

func contentText(content anthropic.Content) string {
	blocks := content.AsBlocks()
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropic.Block
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.*$`)

func extractCommandPrefix(command string) string {
	if strings.Contains(command, "`") || strings.Contains(command, "$(") {
		return "command_injection_detected"
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "none"
	}
	start := 0
	for start < len(parts) && envAssignment.MatchString(parts[start]) {
		start++
	}
	if start >= len(parts) {
		return "none"
	}
	cmd := parts[start:]
	first := cmd[0]
	twoWord := map[string]bool{"git": true, "npm": true, "docker": true, "kubectl": true, "cargo": true, "go": true, "pip": true, "yarn": true}
	if twoWord[first] && len(cmd) > 1 && !strings.HasPrefix(cmd[1], "-") {
		return first + " " + cmd[1]
	}
	if start > 0 {
		return strings.Join(parts[:start], " ") + " " + first
	}
	return first
}

func extractFilepaths(command, output string) string {
	_ = output
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "<filepaths>\n</filepaths>"
	}
	start := 0
	for start < len(parts) && envAssignment.MatchString(parts[start]) {
		start++
	}
	if start >= len(parts) {
		return "<filepaths>\n</filepaths>"
	}
	cmd := parts[start:]
	base := strings.ToLower(filepath.Base(cmd[0]))
	listing := map[string]bool{"ls": true, "dir": true, "find": true, "tree": true, "pwd": true, "cd": true, "mkdir": true, "rmdir": true, "rm": true}
	reading := map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "bat": true, "type": true}
	if listing[base] {
		return "<filepaths>\n</filepaths>"
	}
	var paths []string
	if reading[base] {
		for _, part := range cmd[1:] {
			if !strings.HasPrefix(part, "-") {
				paths = append(paths, part)
			}
		}
	} else if base == "grep" {
		positionals := grepPositionals(cmd[1:])
		if len(positionals) > 1 {
			paths = positionals[1:]
		}
	}
	if len(paths) == 0 {
		return "<filepaths>\n</filepaths>"
	}
	return "<filepaths>\n" + strings.Join(paths, "\n") + "\n</filepaths>"
}

func grepPositionals(args []string) []string {
	flagsWithArgs := map[string]bool{"-e": true, "-f": true, "-m": true, "-A": true, "-B": true, "-C": true}
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if flagsWithArgs[arg] && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out
}
