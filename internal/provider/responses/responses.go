// Package responses is the OpenAI Responses API (/v1/responses) egress adapter.
package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/relaycode/relaycode/internal/anthropic"
	"github.com/relaycode/relaycode/internal/config"
	"github.com/relaycode/relaycode/internal/provider"
	"github.com/relaycode/relaycode/internal/session"
	"github.com/relaycode/relaycode/internal/sse"
)

type Adapter struct {
	pc           config.ProviderConfig
	client       *http.Client
	sem          chan struct{}
	store        *session.Store
	providerName string
}

func New(pc config.ProviderConfig) (provider.Adapter, error) {
	if pc.APIKey == "" {
		return nil, fmt.Errorf("openai_responses: api_key is empty")
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

// SetSession wires a session store after construction. Adapters without a
// store behave statelessly (full replay every request).
func (a *Adapter) SetSession(store *session.Store, providerName string) {
	a.store = store
	a.providerName = providerName
}

// Stream translates an Anthropic Request to POST /v1/responses and converts
// the streamed response back into Anthropic SSE events via b.
func (a *Adapter) Stream(ctx context.Context, req *anthropic.Request, upstreamModel string, b *sse.Builder) error {
	if a.sem != nil {
		select {
		case a.sem <- struct{}{}:
			defer func() { <-a.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.streamOnce(ctx, req, upstreamModel, b, false)
}

func (a *Adapter) streamOnce(
	ctx context.Context,
	req *anthropic.Request,
	upstreamModel string,
	b *sse.Builder,
	forceFull bool,
) error {
	_ = forceFull

	var (
		lookup        *session.Lookup
		cacheKey      string
		promptCacheOk bool
	)
	if a.store != nil {
		var err error
		lookup, err = a.store.Prepare(a.providerName, upstreamModel, req)
		if err != nil {
			return err
		}
		// Prefer the conversation's own session id (Claude Code's
		// metadata.user_id.session_id) so every turn of the same chat
		// shares one cache_key and upstream reuses its prefix state.
		// Fall back to the instructions+tools fingerprint when no session
		// id is available (raw curl clients, tests).
		if sid := req.SessionID(); sid != "" {
			cacheKey = sid
			promptCacheOk = true
		} else {
			cacheKey = lookup.InstructionsHash + ":" + lookup.ToolsHash
			promptCacheOk = cacheKey != ":"
		}
	}

	body, err := buildRequest(req, upstreamModel, a.pc.ExperimentalPassthroughServerTools)
	if err != nil {
		return err
	}
	if promptCacheOk {
		body["prompt_cache_key"] = cacheKey
	}
	raw, _ := json.Marshal(body)

	extraHeaders, err := codexAuthHeaders(a.pc.CodexAuthPath)
	if err != nil {
		b.Start()
		b.FinishWithError(err.Error())
		return nil
	}
	reader, closer, err := provider.PostStreamWithClient(ctx, a.client, a.pc.MaxRetries, a.pc.BaseURL, "/responses", a.pc.APIKey, "Authorization", extraHeaders, raw)
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

	stripper := tagStripper{}
	stopReason := "end_turn"
	outputTokens := 0
	inputTokens := 0
	cachedTokens := 0
	newResponseID := ""
	sawUpstreamError := false
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
			if header.Delta == "" {
				return nil
			}
			if safe := stripper.Feed(header.Delta); safe != "" {
				b.EmitText(safe)
			}

		case "response.output_text.done":
			stripper.Reset()

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
			if handled, err := emitWebSearchCall(ev.Data, b); err != nil {
				return nil
			} else if handled {
				return nil
			}
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
					ID    string `json:"id"`
					Usage struct {
						InputTokens        int `json:"input_tokens"`
						OutputTokens       int `json:"output_tokens"`
						InputTokensDetails struct {
							CachedTokens int `json:"cached_tokens"`
						} `json:"input_tokens_details"`
					} `json:"usage"`
					Output []struct {
						Type string `json:"type"`
					} `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err == nil {
				newResponseID = wrap.Response.ID
				outputTokens = wrap.Response.Usage.OutputTokens
				inputTokens = wrap.Response.Usage.InputTokens
				cachedTokens = wrap.Response.Usage.InputTokensDetails.CachedTokens
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
			sawUpstreamError = true
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

	if tail := stripper.Flush(); tail != "" {
		b.EmitText(tail)
	}

	if outputTokens > 0 {
		b.SetOutputTokens(outputTokens)
	}
	b.SetStopReason(stopReason)
	b.Finish()

	if !sawUpstreamError && a.store != nil && lookup != nil && newResponseID != "" {
		a.store.Commit(lookup, a.providerName, upstreamModel, len(req.Messages), newResponseID, inputTokens, outputTokens)
		a.store.Stats.InputTokens.Add(int64(inputTokens))
		a.store.Stats.OutputTokens.Add(int64(outputTokens))
		if cachedTokens > 0 {
			a.store.Stats.Hits.Add(1)
			log.Printf("responses: cache_hit provider=%s model=%s cached_tokens=%d input_tokens=%d output_tokens=%d resp=%s",
				a.providerName, upstreamModel, cachedTokens, inputTokens, outputTokens, newResponseID)
		} else {
			a.store.Stats.Misses.Add(1)
			log.Printf("responses: cache_miss provider=%s model=%s input_tokens=%d output_tokens=%d resp=%s",
				a.providerName, upstreamModel, inputTokens, outputTokens, newResponseID)
		}
	}
	return nil
}

func codexAuthHeaders(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read codex auth: %w", err)
	}
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, fmt.Errorf("parse codex auth: %w", err)
	}
	if auth.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("codex auth %s missing tokens.access_token", path)
	}
	headers := map[string]string{"Authorization": "Bearer " + auth.Tokens.AccessToken}
	if auth.Tokens.AccountID != "" {
		headers["ChatGPT-Account-ID"] = auth.Tokens.AccountID
	}
	return headers, nil
}

func emitWebSearchCall(data string, b *sse.Builder) (bool, error) {
	var wrap struct {
		Item struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Action struct {
				Query string `json:"query"`
			} `json:"action"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &wrap); err != nil {
		return false, err
	}
	if wrap.Item.Type != "web_search_call" || wrap.Item.ID == "" {
		return false, nil
	}
	query := wrap.Item.Action.Query
	b.StartServerTool(wrap.Item.ID, "web_search")
	b.EmitToolInput(wrap.Item.ID, fmt.Sprintf(`{"query":%q}`, query))
	b.StopTool(wrap.Item.ID)
	b.EmitWebSearchResult(wrap.Item.ID, []map[string]string{})
	return true, nil
}

// ---- Request translation ----

// Responses API input items (subset we emit).
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
	Type              string          `json:"type"`
	Name              string          `json:"name,omitempty"`
	Description       string          `json:"description,omitempty"`
	Parameters        json.RawMessage `json:"parameters,omitempty"`
	ExternalWebAccess *bool           `json:"external_web_access,omitempty"`
}

func buildRequest(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, error) {
	items, err := convertMessagesToItems(r.Messages, passthroughServerTools)
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
	applyCommonFields(body, r, passthroughServerTools)
	return body, nil
}

func applyCommonFields(body map[string]any, r *anthropic.Request, passthroughServerTools bool) {
	if r.MaxTokens > 0 {
		body["max_output_tokens"] = r.MaxTokens
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	// Match openai/codex HTTP request shape: tool_choice and parallel_tool_calls
	// are always present; store defaults to false on openai.com.
	body["tool_choice"] = responsesToolChoice(r.ToolChoice)
	body["parallel_tool_calls"] = true
	if _, set := body["store"]; !set {
		body["store"] = false
	}
	if len(r.Tools) > 0 {
		upstreamTools := anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)
		if len(upstreamTools) > 0 {
			tools := make([]toolDecl, 0, len(upstreamTools))
			for _, t := range upstreamTools {
				tools = append(tools, toResponsesToolDecl(t))
			}
			body["tools"] = tools
		}
	}
}

func toResponsesToolDecl(t anthropic.Tool) toolDecl {
	return toolDecl{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.InputSchema,
	}
}

func responsesToolChoice(raw json.RawMessage) any {
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
	case "auto", "any":
		return "auto"
	case "none":
		return "none"
	case "tool":
		if choice.Name != "" {
			return map[string]any{"type": "function", "name": choice.Name}
		}
	}
	return "auto"
}

func convertMessagesToItems(msgs []anthropic.Message, passthroughServerTools bool) ([]inputItem, error) {
	var out []inputItem
	for _, m := range msgs {
		blocks := anthropic.BlocksForUpstream(m.Content.AsBlocks(), passthroughServerTools)
		switch m.Role {
		case "user":
			msg := inputItem{Type: "message", Role: "user"}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					msg.Content = append(msg.Content, inputContentPart{Type: "input_text", Text: b.Text})
				case "tool_result", "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "computer_use_tool_result", "mcp_tool_result":
					if b.Type == "web_search_tool_result" {
						continue
					}
					if b.ToolUseID == "" {
						continue
					}
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
				case "tool_use", "server_tool_use", "mcp_tool_use":
					if b.Type == "server_tool_use" && b.Name == "web_search" {
						continue
					}
					if b.ID == "" || b.Name == "" {
						continue
					}
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
