// Package responses is the OpenAI Responses API (/v1/responses) egress adapter.
package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/config"
	"github.com/relaycode/relaycode/internal/provider"
	"github.com/relaycode/relaycode/internal/sse"
)

type Adapter struct {
	pc config.ProviderConfig
}

func New(pc config.ProviderConfig) (provider.Adapter, error) {
	if pc.APIKey == "" {
		return nil, fmt.Errorf("openai_responses: api_key is empty")
	}
	return &Adapter{pc: pc}, nil
}

// Stream translates an Anthropic Request to POST /v1/responses and converts
// the streamed response back into Anthropic SSE events via b.
func (a *Adapter) Stream(ctx context.Context, req *anthropic.Request, upstreamModel string, b *sse.Builder) error {
	body, err := buildRequest(req, upstreamModel)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(body)

	reader, closer, err := provider.PostStream(ctx, a.pc.BaseURL, "/responses", a.pc.APIKey, "Authorization", raw)
	if err != nil {
		b.Start()
		b.FinishWithError(err.Error())
		return nil
	}
	defer closer.Close()
	b.Start()

	type itemState struct {
		kind   string
		callID string
		name   string
	}
	items := map[string]*itemState{}

	stopReason := "end_turn"
	outputTokens := 0
	upstreamErrMsg := ""

	err = provider.IterSSE(reader, func(ev provider.SSEEvent) error {
		if ev.Data == "" {
			return nil
		}
		var header struct {
			Type   string `json:"type"`
			Delta  string `json:"delta,omitempty"`
			ItemID string `json:"item_id,omitempty"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &header); err != nil {
			return nil
		}

		switch header.Type {
		case "response.output_item.added":
			var wrap struct {
				Item struct {
					ID     string `json:"id"`
					Type   string `json:"type"`
					CallID string `json:"call_id,omitempty"`
					Name   string `json:"name,omitempty"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err != nil {
				return nil
			}
			st := &itemState{kind: wrap.Item.Type, callID: wrap.Item.CallID, name: wrap.Item.Name}
			items[wrap.Item.ID] = st
			if st.kind == "function_call" && st.callID != "" && st.name != "" {
				b.StartTool(st.callID, st.name)
			}

		case "response.output_text.delta":
			if header.Delta != "" {
				b.EmitText(header.Delta)
			}

		case "response.reasoning_summary_text.delta", "response.reasoning.delta", "response.reasoning_text.delta":
			if header.Delta != "" {
				b.EmitThinking(header.Delta)
			}

		case "response.function_call_arguments.delta":
			st := items[header.ItemID]
			if st == nil || st.callID == "" {
				return nil
			}
			b.EmitToolInput(st.callID, header.Delta)

		case "response.function_call_arguments.done":
			st := items[header.ItemID]
			if st == nil || st.callID == "" {
				return nil
			}
			b.StopTool(st.callID)

		case "response.output_item.done":
			var wrap struct {
				Item struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err != nil {
				return nil
			}
			if st, ok := items[wrap.Item.ID]; ok && st.kind == "function_call" && st.callID != "" {
				b.StopTool(st.callID)
			}

		case "response.completed":
			var wrap struct {
				Response struct {
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
					Output []struct {
						Type string `json:"type"`
					} `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err == nil {
				outputTokens = wrap.Response.Usage.OutputTokens
				for _, o := range wrap.Response.Output {
					if o.Type == "function_call" {
						stopReason = "tool_use"
						break
					}
				}
			}

		case "response.incomplete":
			stopReason = "max_tokens"

		case "response.failed", "response.error":
			var wrap struct {
				Response struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"response"`
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			msg := "upstream responses error"
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err == nil {
				if wrap.Response.Error.Message != "" {
					msg = wrap.Response.Error.Message
				} else if wrap.Error.Message != "" {
					msg = wrap.Error.Message
				}
			}
			upstreamErrMsg = msg
			return provider.ErrStopSSE
		}
		return nil
	})
	if err != nil {
		b.FinishWithError(err.Error())
		return nil
	}
	if upstreamErrMsg != "" {
		b.FinishWithError(upstreamErrMsg)
		return nil
	}

	if outputTokens > 0 {
		b.SetOutputTokens(outputTokens)
	}
	b.SetStopReason(stopReason)
	b.Finish()
	return nil
}

// ---- Request translation ----

type inputItem struct {
	Type    string             `json:"type"`
	Role    string             `json:"role,omitempty"`
	Content []inputContentPart `json:"content,omitempty"`
	Name    string             `json:"name,omitempty"`
	CallID  string             `json:"call_id,omitempty"`
	Args    string             `json:"arguments,omitempty"`
	Output  string             `json:"output,omitempty"`
}

type inputContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolDecl struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildRequest(r *anthropic.Request, model string) (map[string]any, error) {
	items, err := convertMessagesToItems(r.Messages)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":  model,
		"input":  items,
		"stream": true,
	}
	if sysText, err := anthropic.SystemText(r.System); err != nil {
		return nil, err
	} else if sysText != "" {
		body["instructions"] = sysText
	}
	applyCommonFields(body, r)
	return body, nil
}

// applyCommonFields mirrors openai/codex CLI's Responses API request shape:
// tool_choice and parallel_tool_calls are always present; store defaults
// to false (matching openai.com's Codex client behavior).
func applyCommonFields(body map[string]any, r *anthropic.Request) {
	if r.MaxTokens > 0 {
		body["max_output_tokens"] = r.MaxTokens
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	body["tool_choice"] = "auto"
	body["parallel_tool_calls"] = true
	if _, set := body["store"]; !set {
		body["store"] = false
	}
	if len(r.Tools) > 0 {
		tools := make([]toolDecl, 0, len(r.Tools))
		for _, t := range r.Tools {
			tools = append(tools, toolDecl{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
		body["tools"] = tools
	}
}

func convertMessagesToItems(msgs []anthropic.Message) ([]inputItem, error) {
	var out []inputItem
	for _, m := range msgs {
		blocks := m.Content.AsBlocks()
		switch m.Role {
		case "user":
			msg := inputItem{Type: "message", Role: "user"}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					msg.Content = append(msg.Content, inputContentPart{Type: "input_text", Text: b.Text})
				case "tool_result":
					if len(msg.Content) > 0 {
						out = append(out, msg)
						msg = inputItem{Type: "message", Role: "user"}
					}
					text, err := anthropic.ToolResultText(b.Content)
					if err != nil {
						return nil, err
					}
					out = append(out, inputItem{
						Type:   "function_call_output",
						CallID: b.ToolUseID,
						Output: text,
					})
				case "image":
					return nil, fmt.Errorf("image user blocks not supported by openai_responses adapter")
				}
			}
			if len(msg.Content) > 0 {
				out = append(out, msg)
			}
		case "assistant":
			var textParts []string
			var pendingCalls []inputItem
			for _, b := range blocks {
				switch b.Type {
				case "text":
					textParts = append(textParts, b.Text)
				case "thinking", "redacted_thinking":
					// drop replay: Responses API does not accept raw thinking blocks
				case "tool_use":
					args := "{}"
					if len(b.Input) > 0 {
						args = string(b.Input)
					}
					pendingCalls = append(pendingCalls, inputItem{
						Type:   "function_call",
						Name:   b.Name,
						CallID: b.ID,
						Args:   args,
					})
				}
			}
			if text := strings.TrimSpace(strings.Join(textParts, "\n\n")); text != "" {
				out = append(out, inputItem{
					Type: "message",
					Role: "assistant",
					Content: []inputContentPart{
						{Type: "output_text", Text: text},
					},
				})
			}
			out = append(out, pendingCalls...)
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}
	return out, nil
}
