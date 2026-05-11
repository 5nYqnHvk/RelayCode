// Package chat is the OpenAI Chat Completions egress adapter.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/provider"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
	"github.com/5nYqnHvk/RelayCode/internal/streamparse"
)

type Adapter struct {
	pc     config.ProviderConfig
	client *http.Client
	sem    chan struct{}
}

func New(pc config.ProviderConfig) (provider.Adapter, error) {
	if pc.APIKey == "" {
		return nil, fmt.Errorf("openai_chat: api_key is empty")
	}
	client, err := provider.HTTPClient(pc)
	if err != nil {
		return nil, err
	}
	var sem chan struct{}
	if pc.MaxConcurrency > 0 {
		sem = make(chan struct{}, pc.MaxConcurrency)
	}
	return &Adapter{pc: pc, client: client, sem: sem}, nil
}

// Stream translates an Anthropic Request to OpenAI /chat/completions and
// converts the streamed response back into Anthropic SSE events via b.
func (a *Adapter) Stream(ctx context.Context, req *anthropic.Request, upstreamModel string, b *sse.Builder) error {
	if a.sem != nil {
		select {
		case a.sem <- struct{}{}:
			defer func() { <-a.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	body, aliases, err := buildRequestWithAliases(req, upstreamModel, a.pc.ExperimentalPassthroughServerTools)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(body)

	b.Start()

	reader, closer, err := provider.PostStreamWithClient(ctx, a.client, a.pc.MaxRetries, a.pc.BaseURL, "/chat/completions", a.pc.APIKey, "Authorization", nil, raw)
	if err != nil {
		b.FinishWithError(err.Error())
		return nil
	}
	defer closer.Close()

	type choiceDelta struct {
		Role             string `json:"role,omitempty"`
		Content          string `json:"content,omitempty"`
		ReasoningContent string `json:"reasoning_content,omitempty"`
		ToolCalls        []struct {
			Index    int    `json:"index"`
			ID       string `json:"id,omitempty"`
			Type     string `json:"type,omitempty"`
			Function struct {
				Name      string `json:"name,omitempty"`
				Arguments string `json:"arguments,omitempty"`
			} `json:"function,omitempty"`
		} `json:"tool_calls,omitempty"`
	}
	type chunk struct {
		Choices []struct {
			Delta        choiceDelta `json:"delta"`
			FinishReason string      `json:"finish_reason,omitempty"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage,omitempty"`
	}

	// tool-call index -> synthetic Anthropic tool id / name
	type toolTrack struct {
		id   string
		name string
	}
	tools := map[int]*toolTrack{}
	aliasBuffers := map[int]string{}
	flushAliasBuffers := func() {
		for idx, buffered := range aliasBuffers {
			track := tools[idx]
			if track == nil || track.id == "" || track.name == "" {
				continue
			}
			restored, ok := restoreToolArgs(idx, track.name, buffered, aliases, map[int]string{})
			if ok {
				b.EmitToolInput(track.id, restored)
				delete(aliasBuffers, idx)
			}
		}
	}

	finishReason := ""
	completionTokens := 0
	thinkParser := streamparse.ThinkTagParser{}
	toolParser := streamparse.HeuristicToolParser{}
	emitToolCalls := func(calls []streamparse.ToolCall) {
		for _, call := range calls {
			if call.ID == "" || call.Name == "" {
				continue
			}
			raw, _ := json.Marshal(call.Input)
			b.StartTool(call.ID, call.Name)
			b.EmitToolInput(call.ID, string(raw))
			b.StopTool(call.ID)
		}
	}
	emitParsedText := func(text string) {
		for _, chunk := range thinkParser.Feed(text) {
			if chunk.Kind == streamparse.ThinkingChunk {
				b.EmitThinking(chunk.Content)
			} else {
				safe, calls := toolParser.Feed(chunk.Content)
				b.EmitText(safe)
				emitToolCalls(calls)
			}
		}
	}

	err = provider.IterSSE(reader, func(ev provider.SSEEvent) error {
		if ev.Data == "" || ev.Data == "[DONE]" {
			return nil
		}
		var c chunk
		if err := json.Unmarshal([]byte(ev.Data), &c); err != nil {
			return nil
		}
		if c.Usage != nil {
			completionTokens = c.Usage.CompletionTokens
		}
		for _, ch := range c.Choices {
			d := ch.Delta
			if d.ReasoningContent != "" {
				b.EmitThinking(d.ReasoningContent)
			}
			if d.Content != "" {
				emitParsedText(d.Content)
			}
			for _, tc := range d.ToolCalls {
				t, ok := tools[tc.Index]
				if !ok {
					t = &toolTrack{id: tc.ID, name: tc.Function.Name}
					tools[tc.Index] = t
				}
				if tc.ID != "" && t.id == "" {
					t.id = tc.ID
				}
				if tc.Function.Name != "" && t.name == "" {
					t.name = tc.Function.Name
				}
				if t.id == "" || t.name == "" {
					continue
				}
				b.StartTool(t.id, t.name)
				if tc.Function.Arguments != "" {
					if restored, ok := restoreToolArgs(tc.Index, t.name, tc.Function.Arguments, aliases, aliasBuffers); ok {
						b.EmitToolInput(t.id, restored)
					}
				}
			}
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		b.FinishWithError(err.Error())
		return nil
	}

	if tail := thinkParser.Flush(); tail != nil {
		if tail.Kind == streamparse.ThinkingChunk {
			b.EmitThinking(tail.Content)
		} else {
			safe, calls := toolParser.Feed(tail.Content)
			b.EmitText(safe)
			emitToolCalls(calls)
		}
	}
	emitToolCalls(toolParser.Flush())
	flushAliasBuffers()
	if completionTokens > 0 {
		b.SetOutputTokens(completionTokens)
	}
	b.SetStopReason(mapStopReason(finishReason))
	b.Finish()
	return nil
}

func mapStopReason(openai string) string {
	switch openai {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	}
	return "end_turn"
}

type openaiMessage struct {
	Role       string          `json:"role"`
	Content    any             `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []openaiToolRef `json:"tool_calls,omitempty"`
}

type openaiToolRef struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function openaiToolRefFn `json:"function"`
}

type openaiToolRefFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string         `json:"type"`
	Function openaiToolDecl `json:"function"`
}

type openaiToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildRequest(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, error) {
	body, _, err := buildRequestWithAliases(r, model, passthroughServerTools)
	return body, err
}

func buildRequestWithAliases(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, map[string]map[string]string, error) {
	messages, err := convertMessages(r, passthroughServerTools)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	}
	if r.MaxTokens > 0 {
		body["max_tokens"] = r.MaxTokens
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if len(r.StopSequences) > 0 {
		body["stop"] = r.StopSequences
	}
	if effort, ok := r.ReasoningEffort(); ok {
		body["reasoning_effort"] = effort
	}
	aliases := map[string]map[string]string{}
	if len(r.Tools) > 0 {
		upstreamTools := anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)
		if len(upstreamTools) > 0 {
			tools := make([]openaiTool, 0, len(upstreamTools))
			for _, t := range upstreamTools {
				params, toolAliases := sanitizeToolParameters(t.InputSchema)
				if len(toolAliases) > 0 {
					aliases[t.Name] = toolAliases
				}
				tools = append(tools, openaiTool{
					Type: "function",
					Function: openaiToolDecl{
						Name:        t.Name,
						Description: t.Description,
						Parameters:  params,
					},
				})
			}
			body["tools"] = tools
		}
	}
	return body, aliases, nil
}

func sanitizeToolParameters(raw json.RawMessage) (json.RawMessage, map[string]string) {
	if len(raw) == 0 {
		return raw, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw, nil
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return raw, nil
	}
	aliases := map[string]string{}
	for name, value := range props {
		if name != "type" {
			continue
		}
		alias := "_fcc_arg_type"
		if _, exists := props[alias]; exists {
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("_fcc_arg_type_%d", i)
				if _, taken := props[candidate]; !taken {
					alias = candidate
					break
				}
			}
		}
		props[alias] = value
		delete(props, name)
		aliases[alias] = name
		if required, ok := schema["required"].([]any); ok {
			for i, item := range required {
				if item == name {
					required[i] = alias
				}
			}
		}
	}
	if len(aliases) == 0 {
		return raw, nil
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return raw, nil
	}
	return out, aliases
}

func restoreToolArgs(index int, toolName, args string, aliases map[string]map[string]string, buffers map[int]string) (string, bool) {
	toolAliases := aliases[toolName]
	if len(toolAliases) == 0 {
		return args, true
	}
	buffered := buffers[index] + args
	var parsed any
	if err := json.Unmarshal([]byte(buffered), &parsed); err != nil {
		buffers[index] = buffered
		return "", false
	}
	delete(buffers, index)
	restored := restoreAliasValue(parsed, toolAliases)
	out, err := json.Marshal(restored)
	if err != nil {
		return args, true
	}
	return string(out), true
}

func restoreAliasValue(value any, aliases map[string]string) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if original, ok := aliases[key]; ok {
				key = original
			}
			out[key] = restoreAliasValue(item, aliases)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = restoreAliasValue(item, aliases)
		}
		return out
	}
	return value
}

func convertMessages(r *anthropic.Request, passthroughServerTools bool) ([]openaiMessage, error) {
	var out []openaiMessage

	if sysText, err := anthropic.SystemText(r.System); err != nil {
		return nil, err
	} else if sysText != "" {
		out = append(out, openaiMessage{Role: "system", Content: sysText})
	}

	for _, m := range r.Messages {
		blocks := anthropic.BlocksForUpstream(m.Content.AsBlocks(), passthroughServerTools)
		switch m.Role {
		case "user":
			msgs, err := convertUserBlocks(blocks)
			if err != nil {
				return nil, err
			}
			out = append(out, msgs...)
		case "assistant":
			msgs, err := convertAssistantBlocks(blocks)
			if err != nil {
				return nil, err
			}
			out = append(out, msgs...)
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}
	return out, nil
}

func convertUserBlocks(blocks []anthropic.Block) ([]openaiMessage, error) {
	var out []openaiMessage
	var textParts []string
	flush := func() {
		if len(textParts) == 0 {
			return
		}
		out = append(out, openaiMessage{Role: "user", Content: strings.Join(textParts, "\n")})
		textParts = nil
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "tool_result", "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "computer_use_tool_result", "mcp_tool_result":
			if b.ToolUseID == "" {
				continue
			}
			flush()
			text, err := anthropic.ToolResultText(b.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, openaiMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    text,
			})
		case "image":
			return nil, fmt.Errorf("image user blocks not supported by openai_chat adapter")
		default:
			if anthropic.IsServerToolBlock(b) {
				// Server-tool records from a prior turn are opaque to the
				// upstream; drop rather than forward broken structure.
				continue
			}
		}
	}
	flush()
	return out, nil
}

func convertAssistantBlocks(blocks []anthropic.Block) ([]openaiMessage, error) {
	var preText []string
	var postText []string
	var toolCalls []openaiToolRef
	seenTool := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if seenTool {
				postText = append(postText, b.Text)
			} else {
				preText = append(preText, b.Text)
			}
		case "thinking", "redacted_thinking":
			// drop: OpenAI chat does not accept Anthropic thinking blocks
		case "tool_use", "server_tool_use", "mcp_tool_use":
			if b.ID == "" || b.Name == "" {
				continue
			}
			seenTool = true
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			toolCalls = append(toolCalls, openaiToolRef{
				ID:   b.ID,
				Type: "function",
				Function: openaiToolRefFn{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	msg := openaiMessage{
		Role:      "assistant",
		Content:   strings.Join(preText, "\n\n"),
		ToolCalls: toolCalls,
	}
	if msg.Content == "" && len(toolCalls) == 0 {
		msg.Content = " "
	}
	out := []openaiMessage{msg}
	if text := strings.Join(postText, "\n\n"); strings.TrimSpace(text) != "" {
		out = append(out, openaiMessage{Role: "assistant", Content: text})
	}
	return out, nil
}
