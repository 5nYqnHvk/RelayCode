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
	Betas         []string        `json:"betas,omitempty"`
	// ContextManagement and ExtraFields keep Claude Code beta/body params intact
	// for native Anthropic egress. OpenAI adapters intentionally ignore them.
	ContextManagement json.RawMessage            `json:"context_management,omitempty"`
	ExtraFields       map[string]json.RawMessage `json:"-"`
}

// OutputConfig carries Anthropic output configuration fields.
type OutputConfig struct {
	Effort      any                        `json:"effort,omitempty"`
	ExtraFields map[string]json.RawMessage `json:"-"`
}

func (r *Request) UnmarshalJSON(b []byte) error {
	type requestAlias Request
	var aux requestAlias
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*r = Request(aux)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for _, key := range knownRequestFields() {
		delete(raw, key)
	}
	if len(raw) > 0 {
		r.ExtraFields = raw
	} else {
		r.ExtraFields = nil
	}
	return nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	type requestAlias Request
	raw, err := json.Marshal(requestAlias(r))
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	for key, value := range r.ExtraFields {
		if len(value) == 0 {
			continue
		}
		if _, exists := body[key]; !exists {
			body[key] = value
		}
	}
	return json.Marshal(body)
}

func knownRequestFields() []string {
	return []string{
		"model", "max_tokens", "system", "messages", "tools", "tool_choice",
		"temperature", "top_p", "top_k", "stop_sequences", "stream",
		"thinking", "output_config", "metadata", "betas", "context_management",
	}
}

func (o *OutputConfig) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if v, ok := raw["effort"]; ok {
		var effort any
		if err := json.Unmarshal(v, &effort); err != nil {
			return err
		}
		o.Effort = effort
		delete(raw, "effort")
	}
	if len(raw) > 0 {
		o.ExtraFields = raw
	} else {
		o.ExtraFields = nil
	}
	return nil
}

func (o OutputConfig) MarshalJSON() ([]byte, error) {
	body := map[string]json.RawMessage{}
	if o.Effort != nil {
		raw, err := json.Marshal(o.Effort)
		if err != nil {
			return nil, err
		}
		body["effort"] = raw
	}
	for key, value := range o.ExtraFields {
		if len(value) == 0 {
			continue
		}
		if _, exists := body[key]; !exists {
			body[key] = value
		}
	}
	return json.Marshal(body)
}

func (o *OutputConfig) RawField(name string) json.RawMessage {
	if o == nil || o.ExtraFields == nil {
		return nil
	}
	return o.ExtraFields[name]
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

func (r *Request) HasToolSearchBeta() bool {
	if r == nil {
		return false
	}
	for _, beta := range r.Betas {
		if strings.Contains(beta, "tool-search") || strings.Contains(beta, "advanced-tool-use") {
			return true
		}
	}
	return false
}

func (r *Request) UnsupportedOpenAIFields() []string {
	if r == nil {
		return nil
	}
	var fields []string
	if len(r.ContextManagement) > 0 {
		fields = append(fields, "context_management")
	}
	for key := range r.ExtraFields {
		fields = append(fields, key)
	}
	return fields
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
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Caller json.RawMessage `json:"caller,omitempty"`

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
	Strict      *bool           `json:"strict,omitempty"`
}

// IsServerTool reports whether this tool is an Anthropic-managed server tool
// (web_search / web_fetch / code execution / etc). These must be stripped
// from requests forwarded to non-Anthropic backends.
func (t Tool) IsServerTool() bool {
	if isServerToolType(t.Type) {
		return true
	}
	// Legacy/reduced Anthropic server-tool declarations may arrive with only a
	// server name. Do not classify explicitly custom/function tools by name;
	// users can legitimately expose their own web_search/web_fetch functions.
	return t.Type == "" && len(t.InputSchema) == 0 && isServerToolName(t.Name)
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
	return DegradeServerToolBlocks(blocks)
}

func DegradeServerToolBlocks(blocks []Block) []Block {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		if !IsServerToolBlock(b) {
			out = append(out, b)
			continue
		}
		out = append(out, Block{Type: "text", Text: serverToolSummary(b)})
	}
	return out
}

func serverToolSummary(b Block) string {
	name := b.Name
	if name == "" {
		name = b.Type
	}
	id := b.ID
	if id == "" {
		id = b.ToolUseID
	}
	var detail string
	if len(b.Input) > 0 {
		detail = string(b.Input)
	} else if len(b.Content) > 0 {
		text, err := ToolResultText(b.Content)
		if err == nil {
			detail = text
		} else {
			detail = string(b.Content)
		}
	}
	if detail != "" {
		return fmt.Sprintf("[Anthropic server/MCP tool history preserved: %s id=%s detail=%s]", name, id, detail)
	}
	return fmt.Sprintf("[Anthropic server/MCP tool history preserved: %s id=%s]", name, id)
}

// NormalizeMessagesForUpstream applies the defensive transcript repairs Claude
// Code performs before API submission: drop invalid thinking tails, remove
// tool-search-only fields when the beta is absent, and repair tool_use/result
// adjacency enough for resumed transcripts to remain API-valid.
func NormalizeMessagesForUpstream(messages []Message, preserveServerTools, preserveToolSearch bool) []Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := filterOrphanedThinkingOnlyMessages(messages)
	filtered = filterTrailingThinkingFromLastAssistant(filtered)
	return ensureToolResultPairing(filtered, preserveServerTools, preserveToolSearch)
}

// NormalizePreviousResponseTail prepares the incremental tail sent alongside
// previous_response_id. Tool results are intentionally preserved even when the
// matching tool_use is not present in this slice: that call already exists in
// the upstream response being continued.
func NormalizePreviousResponseTail(messages []Message, preserveServerTools, preserveToolSearch bool) []Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := filterOrphanedThinkingOnlyMessages(messages)
	filtered = filterTrailingThinkingFromLastAssistant(filtered)
	out := make([]Message, 0, len(filtered))
	for _, m := range filtered {
		blocks := cloneBlocks(m.Content.AsBlocks())
		if !preserveToolSearch {
			blocks = stripToolSearchOnlyFields(blocks)
		}
		if !preserveServerTools {
			blocks = DegradeServerToolBlocks(blocks)
		}
		if len(blocks) == 0 && m.Role == "user" {
			continue
		}
		m.Content = Content{Blocks: blocks}
		out = append(out, m)
	}
	return out
}

func filterOrphanedThinkingOnlyMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		blocks := m.Content.AsBlocks()
		if len(blocks) == 0 || !allThinkingBlocks(blocks) {
			out = append(out, m)
		}
	}
	return out
}

func filterTrailingThinkingFromLastAssistant(messages []Message) []Message {
	if len(messages) == 0 || messages[len(messages)-1].Role != "assistant" {
		return messages
	}
	blocks := messages[len(messages)-1].Content.AsBlocks()
	idx := len(blocks) - 1
	for idx >= 0 && isThinkingBlock(blocks[idx]) {
		idx--
	}
	if idx == len(blocks)-1 {
		return messages
	}
	out := append([]Message(nil), messages...)
	if idx < 0 {
		out[len(out)-1].Content = Content{Blocks: []Block{{Type: "text", Text: "[No message content]"}}}
	} else {
		out[len(out)-1].Content = Content{Blocks: append([]Block(nil), blocks[:idx+1]...)}
	}
	return out
}

func ensureToolResultPairing(messages []Message, preserveServerTools, preserveToolSearch bool) []Message {
	out := make([]Message, 0, len(messages))
	seenToolUses := map[string]bool{}
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		blocks := cloneBlocks(m.Content.AsBlocks())
		if !preserveToolSearch {
			blocks = stripToolSearchOnlyFields(blocks)
		}
		if !preserveServerTools {
			blocks = DegradeServerToolBlocks(blocks)
		}

		if m.Role != "assistant" {
			if m.Role == "user" && hasToolResults(blocks) {
				blocks = stripToolResults(blocks, nil)
				if len(blocks) == 0 {
					text := "[No message content]"
					if len(out) == 0 {
						text = "[Orphaned tool result removed due to conversation resume]"
					}
					blocks = []Block{{Type: "text", Text: text}}
				}
			}
			if len(blocks) > 0 || m.Role != "user" {
				m.Content = Content{Blocks: blocks}
				out = append(out, m)
			}
			continue
		}

		var toolIDs []string
		kept := make([]Block, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "tool_use" {
				if b.ID == "" || b.Name == "" || seenToolUses[b.ID] {
					continue
				}
				seenToolUses[b.ID] = true
				toolIDs = append(toolIDs, b.ID)
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			kept = []Block{{Type: "text", Text: "[Tool use interrupted]"}}
		}
		m.Content = Content{Blocks: kept}
		out = append(out, m)

		if len(toolIDs) == 0 {
			continue
		}
		toolSet := stringSet(toolIDs)
		var existing []string
		var nextBlocks []Block
		if i+1 < len(messages) && messages[i+1].Role == "user" {
			nextBlocks = cloneBlocks(messages[i+1].Content.AsBlocks())
			if !preserveToolSearch {
				nextBlocks = stripToolSearchOnlyFields(nextBlocks)
			}
			if !preserveServerTools {
				nextBlocks = DegradeServerToolBlocks(nextBlocks)
			}
			existing = toolResultIDs(nextBlocks)
		}
		existingSet := stringSet(existing)
		var missing []string
		for _, id := range toolIDs {
			if !existingSet[id] {
				missing = append(missing, id)
			}
		}
		if i+1 < len(messages) && messages[i+1].Role == "user" {
			patched := make([]Block, 0, len(nextBlocks)+len(missing))
			for _, id := range missing {
				patched = append(patched, syntheticToolResult(id))
			}
			patched = append(patched, stripToolResults(nextBlocks, toolSet)...)
			if len(patched) == 0 {
				patched = []Block{{Type: "text", Text: "[No message content]"}}
			}
			next := messages[i+1]
			next.Content = Content{Blocks: patched}
			out = append(out, next)
			i++
		} else if len(missing) > 0 {
			patched := make([]Block, 0, len(missing))
			for _, id := range missing {
				patched = append(patched, syntheticToolResult(id))
			}
			out = append(out, Message{Role: "user", Content: Content{Blocks: patched}})
		}
	}
	return out
}

func cloneBlocks(blocks []Block) []Block {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]Block, len(blocks))
	copy(out, blocks)
	return out
}

func allThinkingBlocks(blocks []Block) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if !isThinkingBlock(b) {
			return false
		}
	}
	return true
}

func isThinkingBlock(b Block) bool {
	return b.Type == "thinking" || b.Type == "redacted_thinking"
}

func stripToolSearchOnlyFields(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		b.Caller = nil
		if b.Type == "tool_result" && len(b.Content) > 0 {
			b.Content = stripToolReferenceBlocks(b.Content)
		}
		out = append(out, b)
	}
	return out
}

func stripToolReferenceBlocks(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || raw[0] != '[' {
		return raw
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return raw
	}
	changed := false
	kept := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if typ, _ := part["type"].(string); typ == "tool_reference" {
			changed = true
			continue
		}
		kept = append(kept, part)
	}
	if !changed {
		return raw
	}
	if len(kept) == 0 {
		kept = []map[string]any{{"type": "text", "text": "[Tool references removed - tool search not enabled]"}}
	}
	buf, err := json.Marshal(kept)
	if err != nil {
		return raw
	}
	return buf
}

func hasToolResults(blocks []Block) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

func stripToolResults(blocks []Block, allowed map[string]bool) []Block {
	out := make([]Block, 0, len(blocks))
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			out = append(out, b)
			continue
		}
		if b.ToolUseID == "" || seen[b.ToolUseID] || allowed == nil || !allowed[b.ToolUseID] {
			continue
		}
		seen[b.ToolUseID] = true
		out = append(out, b)
	}
	return out
}

func toolResultIDs(blocks []Block) []string {
	ids := make([]string, 0, len(blocks))
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Type == "tool_result" && b.ToolUseID != "" && !seen[b.ToolUseID] {
			ids = append(ids, b.ToolUseID)
			seen[b.ToolUseID] = true
		}
	}
	return ids
}

func syntheticToolResult(id string) Block {
	return Block{
		Type:      "tool_result",
		ToolUseID: id,
		Content:   json.RawMessage(`"[Tool result missing due to transcript repair]"`),
		IsError:   true,
	}
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
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
