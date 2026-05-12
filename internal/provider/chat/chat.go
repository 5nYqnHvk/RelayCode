// Package chat is the OpenAI Chat Completions egress adapter.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/provider"
	"github.com/5nYqnHvk/RelayCode/internal/provider/toolargs"
	"github.com/5nYqnHvk/RelayCode/internal/provider/toolguard"
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

	// tool-call index -> synthetic Anthropic tool id / name / args
	type toolTrack struct {
		id      string
		name    string
		args    string
		emitted bool
	}
	tools := map[int]*toolTrack{}
	registry := toolguard.NewRegistry(req.Tools, a.pc.ExperimentalPassthroughServerTools, aliases)

	finishReason := ""
	completionTokens := 0
	emittedToolCall := false
	thinkParser := streamparse.ThinkTagParser{}
	emitParsedText := func(text string) {
		for _, chunk := range thinkParser.Feed(text) {
			if chunk.Kind == streamparse.ThinkingChunk {
				b.EmitThinking(chunk.Content)
			} else {
				b.EmitText(chunk.Content)
			}
		}
	}
	emitValidatedToolCalls := func() bool {
		indices := make([]int, 0, len(tools))
		for idx := range tools {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		emitted := false
		for _, idx := range indices {
			track := tools[idx]
			if track == nil || track.emitted || track.id == "" || track.name == "" {
				continue
			}
			restored, ok := registry.Validate(track.name, track.args)
			if !ok {
				continue
			}
			b.StartTool(track.id, track.name)
			b.EmitToolInput(track.id, restored)
			b.StopTool(track.id)
			track.emitted = true
			emitted = true
		}
		return emitted
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
				t.args += tc.Function.Arguments
			}
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
				if mapStopReason(finishReason) == "tool_use" && emitValidatedToolCalls() {
					emittedToolCall = true
				}
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
			b.EmitText(tail.Content)
		}
	}
	if emitValidatedToolCalls() {
		emittedToolCall = true
	}
	if completionTokens > 0 {
		b.SetOutputTokens(completionTokens)
	}
	stopReason := mapStopReason(finishReason)
	if stopReason == "tool_use" && !emittedToolCall {
		stopReason = "end_turn"
	}
	b.SetStopReason(stopReason)
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
	Strict      *bool           `json:"strict,omitempty"`
}

func buildRequest(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, error) {
	body, _, err := buildRequestWithAliases(r, model, passthroughServerTools)
	return body, err
}

func buildRequestWithAliases(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, map[string]map[string]string, error) {
	if fields := r.UnsupportedOpenAIFields(); len(fields) > 0 {
		return nil, nil, fmt.Errorf("openai_chat does not support Anthropic-only fields: %s", strings.Join(fields, ", "))
	}
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
	body["tool_choice"] = chatToolChoice(r.ToolChoice)
	body["parallel_tool_calls"] = false
	responseFormat, err := chatResponseFormat(r)
	if err != nil {
		return nil, nil, err
	}
	if responseFormat != nil {
		body["response_format"] = responseFormat
	}
	aliases := map[string]map[string]string{}
	if len(r.Tools) > 0 {
		upstreamTools := anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)
		if len(upstreamTools) > 0 {
			tools := make([]openaiTool, 0, len(upstreamTools))
			for _, t := range upstreamTools {
				params, toolAliases := toolargs.SanitizeParameters(t.InputSchema)
				if len(toolAliases) > 0 {
					aliases[t.Name] = toolAliases
				}
				tools = append(tools, openaiTool{
					Type: "function",
					Function: openaiToolDecl{
						Name:        t.Name,
						Description: t.Description,
						Parameters:  params,
						Strict:      t.Strict,
					},
				})
			}
			body["tools"] = tools
		}
	}
	return body, aliases, nil
}

func chatToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return "auto"
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return "auto"
	}
	switch choice.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if choice.Name != "" {
			return map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}
		}
	}
	return "auto"
}

func chatResponseFormat(r *anthropic.Request) (any, error) {
	if r == nil || r.OutputConfig == nil {
		return nil, nil
	}
	format := r.OutputConfig.RawField("format")
	if len(format) == 0 {
		return nil, nil
	}
	var f map[string]any
	if err := json.Unmarshal(format, &f); err != nil {
		return nil, fmt.Errorf("output_config.format: %w", err)
	}
	typ, _ := f["type"].(string)
	if typ == "" {
		return nil, fmt.Errorf("output_config.format.type is required")
	}
	switch typ {
	case "json_schema":
		schema, ok := f["schema"]
		if !ok {
			return nil, fmt.Errorf("output_config.format.schema is required for json_schema")
		}
		name, _ := f["name"].(string)
		if name == "" {
			name = "relaycode_output_schema"
		}
		strict, ok := f["strict"].(bool)
		if !ok {
			strict = true
		}
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": schema,
				"strict": strict,
			},
		}, nil
	case "text", "json_object":
		return map[string]any{"type": typ}, nil
	default:
		return nil, fmt.Errorf("unsupported output_config.format.type %q for openai_chat", typ)
	}
}

func convertMessages(r *anthropic.Request, passthroughServerTools bool) ([]openaiMessage, error) {
	var out []openaiMessage
	normalized := anthropic.NormalizeMessagesForUpstream(r.Messages, passthroughServerTools, r.HasToolSearchBeta())

	if sysText, err := anthropic.SystemText(r.System); err != nil {
		return nil, err
	} else if sysText != "" {
		out = append(out, openaiMessage{Role: "system", Content: sysText})
	}

	for _, m := range normalized {
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
