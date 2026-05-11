// Package chat is the OpenAI Chat Completions egress adapter.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/config"
	"github.com/relaycode/relaycode/internal/provider"
	"github.com/relaycode/relaycode/internal/sse"
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
	body, err := buildRequest(req, upstreamModel, a.pc.ExperimentalPassthroughServerTools)
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

	finishReason := ""
	completionTokens := 0

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
				b.EmitText(d.Content)
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
					b.EmitToolInput(t.id, tc.Function.Arguments)
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
	messages, err := convertMessages(r, passthroughServerTools)
	if err != nil {
		return nil, err
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
	if len(r.Tools) > 0 {
		upstreamTools := anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)
		if len(upstreamTools) > 0 {
			tools := make([]openaiTool, 0, len(upstreamTools))
			for _, t := range upstreamTools {
				tools = append(tools, openaiTool{
					Type: "function",
					Function: openaiToolDecl{
						Name:        t.Name,
						Description: t.Description,
						Parameters:  t.InputSchema,
					},
				})
			}
			body["tools"] = tools
		}
	}
	return body, nil
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
	var textParts []string
	var toolCalls []openaiToolRef
	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking", "redacted_thinking":
			// drop: OpenAI chat does not accept Anthropic thinking blocks
		case "tool_use", "server_tool_use", "mcp_tool_use":
			if b.ID == "" || b.Name == "" {
				continue
			}
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
		Content:   strings.Join(textParts, "\n\n"),
		ToolCalls: toolCalls,
	}
	if msg.Content == "" && len(toolCalls) == 0 {
		msg.Content = " "
	}
	return []openaiMessage{msg}, nil
}
