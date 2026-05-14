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
	"time"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
	"github.com/5nYqnHvk/RelayCode/internal/config"
	"github.com/5nYqnHvk/RelayCode/internal/provider"
	"github.com/5nYqnHvk/RelayCode/internal/provider/toolargs"
	"github.com/5nYqnHvk/RelayCode/internal/provider/toolguard"
	"github.com/5nYqnHvk/RelayCode/internal/session"
	"github.com/5nYqnHvk/RelayCode/internal/sse"
	"github.com/5nYqnHvk/RelayCode/internal/streamparse"
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
	return a.streamOnce(ctx, req, upstreamModel, b, false, a.pc.MaxRetries)
}

func (a *Adapter) streamOnce(
	ctx context.Context,
	req *anthropic.Request,
	upstreamModel string,
	b *sse.Builder,
	forceFull bool,
	streamRetries int,
) error {
	var (
		lookup        *session.Lookup
		cacheKey      string
		promptCacheOk bool
		err           error
	)
	if a.store != nil {
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
		} else if lookup.InstructionsHash != "" && lookup.ToolsHash != "" {
			cacheKey = lookup.InstructionsHash + ":" + lookup.ToolsHash
			promptCacheOk = true
		} else if lookup.InstructionsHash != "" {
			cacheKey = lookup.InstructionsHash
			promptCacheOk = true
		} else if lookup.ToolsHash != "" {
			cacheKey = lookup.ToolsHash
			promptCacheOk = true
		}
	}

	bodyReq := req
	chained := false
	if !forceFull && a.pc.ExperimentalPreviousResponseID && lookup != nil && lookup.Chain != nil && lookup.Chain.ResponseID != "" {
		tailReq := *req
		tailReq.Messages = lookup.Tail
		bodyReq = &tailReq
		chained = true
	}

	var body map[string]any
	var aliases map[string]map[string]string
	mode := responsesCustomToolMode(a.pc.ResponsesCustomToolMode)
	if chained {
		body, aliases, err = buildTailRequestWithOptions(bodyReq, upstreamModel, a.pc.ExperimentalPassthroughServerTools, a.pc.CompactToolResults, mode)
	} else {
		body, aliases, err = buildRequestWithOptions(bodyReq, upstreamModel, a.pc.ExperimentalPassthroughServerTools, a.pc.CompactToolResults, mode)
	}
	if err != nil {
		return err
	}
	if chained {
		body["previous_response_id"] = lookup.Chain.ResponseID
	}
	if a.pc.ExperimentalPreviousResponseID {
		body["store"] = true
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
		if chained && isInvalidPreviousResponseError(err) && a.store != nil && lookup != nil && lookup.Chain != nil {
			a.store.Invalidate(lookup.Chain.Key)
			a.store.Stats.ExpiredInvalid.Add(1)
			a.store.Stats.ForcedReplays.Add(1)
			log.Printf("responses: previous_response_id invalid provider=%s model=%s resp=%s; retrying full replay", a.providerName, upstreamModel, lookup.Chain.ResponseID)
			return a.streamOnce(ctx, req, upstreamModel, b, true, streamRetries)
		}
		b.Start()
		b.FinishWithError(err.Error())
		return nil
	}
	defer closer.Close()
	b.Start()

	type itemState struct {
		index      int
		kind       string
		callID     string
		name       string
		args       string
		toolClosed bool
	}
	items := map[string]*itemState{}
	emittedToolCall := false
	registry := toolguard.NewRegistry(req.Tools, a.pc.ExperimentalPassthroughServerTools, aliases)

	stripper := tagStripper{}
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
	emitToolCall := func(st *itemState, args string) {
		if st == nil || st.toolClosed || !isCallableItem(st.kind) || st.callID == "" || st.name == "" {
			return
		}
		if args == "" {
			args = st.args
		}
		args = normalizeToolArgsForAnthropic(st.kind, args)
		restored, ok := registry.Validate(st.name, args)
		if !ok {
			if os.Getenv("RELAYCODE_DEBUG_UPSTREAM") == "1" {
				log.Printf("responses: DROPPED tool_call name=%s call_id=%s args=%s (schema validation failed)", st.name, st.callID, args)
			} else {
				log.Printf("responses: DROPPED tool_call name=%s call_id=%s (schema validation failed)", st.name, st.callID)
			}
			return
		}
		b.StartTool(st.callID, st.name)
		b.EmitToolInput(st.callID, restored)
		b.StopTool(st.callID)
		st.toolClosed = true
		emittedToolCall = true
	}
	findItem := func(itemID, callID string) *itemState {
		if itemID != "" {
			if st := items[itemID]; st != nil {
				return st
			}
		}
		if callID == "" {
			return nil
		}
		for _, st := range items {
			if st != nil && st.callID == callID {
				return st
			}
		}
		return nil
	}
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
		if os.Getenv("RELAYCODE_DEBUG_UPSTREAM") == "1" {
			preview := ev.Data
			if len(preview) > 500 {
				preview = preview[:500] + "…"
			}
			log.Printf("responses upstream ev: %s", preview)
		}
		var header struct {
			Type   string `json:"type"`
			Delta  string `json:"delta,omitempty"`
			ItemID string `json:"item_id,omitempty"`
			CallID string `json:"call_id,omitempty"`
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
			items[wrap.Item.ID] = &itemState{index: len(items), kind: wrap.Item.Type, callID: wrap.Item.CallID, name: wrap.Item.Name}

		case "response.output_text.delta":
			if header.Delta == "" {
				return nil
			}
			if safe := stripper.Feed(header.Delta); safe != "" {
				emitParsedText(safe)
			}

		case "response.output_text.done":
			stripper.Reset()

		case "response.reasoning_summary_part.added":
			b.EnsureThinking()

		case "response.reasoning_summary_text.delta", "response.reasoning.delta", "response.reasoning_text.delta":
			if header.Delta != "" {
				b.EmitThinking(header.Delta)
			}

		case "response.custom_tool_call_input.delta":
			st := findItem(header.ItemID, header.CallID)
			if st == nil || st.callID == "" {
				return nil
			}
			if st.kind == "" {
				st.kind = "custom_tool_call"
			}
			st.args += header.Delta

		case "response.function_call_arguments.delta":
			st := findItem(header.ItemID, header.CallID)
			if st == nil || st.callID == "" {
				return nil
			}
			st.args += header.Delta

		case "response.function_call_arguments.done":
			var wrap struct {
				Arguments string `json:"arguments,omitempty"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err != nil {
				return nil
			}
			st := findItem(header.ItemID, header.CallID)
			if st == nil || st.callID == "" {
				return nil
			}
			if wrap.Arguments != "" {
				st.args = wrap.Arguments
			}
			if st.args == "" {
				st.args = "{}"
			}
			emitToolCall(st, st.args)

		case "response.output_item.done":
			if handled, err := emitWebSearchCall(ev.Data, b); err != nil {
				return nil
			} else if handled {
				return nil
			}
			var wrap struct {
				Item struct {
					ID        string `json:"id"`
					Type      string `json:"type"`
					CallID    string `json:"call_id,omitempty"`
					Name      string `json:"name,omitempty"`
					Arguments string `json:"arguments,omitempty"`
					Input     string `json:"input,omitempty"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err != nil {
				return nil
			}
			st := items[wrap.Item.ID]
			if st == nil {
				st = &itemState{index: len(items), kind: wrap.Item.Type, callID: wrap.Item.CallID, name: wrap.Item.Name}
				items[wrap.Item.ID] = st
			}
			if st.kind == "" {
				st.kind = wrap.Item.Type
			}
			if st.callID == "" {
				st.callID = wrap.Item.CallID
			}
			if st.name == "" {
				st.name = wrap.Item.Name
			}
			if isCallableItem(st.kind) && st.callID != "" && st.name != "" {
				if wrap.Item.Arguments != "" {
					st.args = wrap.Item.Arguments
				}
				if wrap.Item.Input != "" {
					st.args = wrap.Item.Input
				}
				emitToolCall(st, st.args)
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
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &wrap); err == nil {
				newResponseID = wrap.Response.ID
				outputTokens = wrap.Response.Usage.OutputTokens
				inputTokens = wrap.Response.Usage.InputTokens
				cachedTokens = wrap.Response.Usage.InputTokensDetails.CachedTokens
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
		if !b.HasContent() && streamRetries > 0 && ctx.Err() == nil {
			closer.Close()
			delay := streamRetryDelay(a.pc.MaxRetries - streamRetries + 1)
			log.Printf("responses: stream retry provider=%s model=%s remaining=%d delay=%s error=%q", a.providerName, upstreamModel, streamRetries-1, delay, err.Error())
			if !sleepContext(ctx, delay) {
				b.FinishWithError(ctx.Err().Error())
				return nil
			}
			return a.streamOnce(ctx, req, upstreamModel, b, forceFull, streamRetries-1)
		}
		b.FinishWithError(err.Error())
		return nil
	}
	if upstreamErrMsg != "" {
		if !b.HasContent() && streamRetries > 0 && ctx.Err() == nil && isRetriableUpstreamMessage(upstreamErrMsg) {
			closer.Close()
			delay := streamRetryDelay(a.pc.MaxRetries - streamRetries + 1)
			log.Printf("responses: upstream retry provider=%s model=%s remaining=%d delay=%s error=%q", a.providerName, upstreamModel, streamRetries-1, delay, upstreamErrMsg)
			if !sleepContext(ctx, delay) {
				b.FinishWithError(ctx.Err().Error())
				return nil
			}
			return a.streamOnce(ctx, req, upstreamModel, b, forceFull, streamRetries-1)
		}
		b.FinishWithError(upstreamErrMsg)
		return nil
	}

	if tail := stripper.Flush(); tail != "" {
		emitParsedText(tail)
	}
	if tail := thinkParser.Flush(); tail != nil {
		if tail.Kind == streamparse.ThinkingChunk {
			b.EmitThinking(tail.Content)
		} else {
			b.EmitText(tail.Content)
		}
	}

	if emittedToolCall {
		stopReason = "tool_use"
	}
	if outputTokens > 0 {
		b.SetOutputTokens(outputTokens)
	}
	b.SetStopReason(stopReason)

	if !sawUpstreamError && a.store != nil && lookup != nil && newResponseID != "" {
		a.store.Commit(lookup, a.providerName, upstreamModel, len(req.Messages), newResponseID, inputTokens, outputTokens)
		a.store.Stats.InputTokens.Add(int64(inputTokens))
		a.store.Stats.OutputTokens.Add(int64(outputTokens))
		if chained {
			a.store.Stats.Hits.Add(1)
			log.Printf("responses: session_chain provider=%s model=%s prev=%s tail_messages=%d total_messages=%d cached_tokens=%d input_tokens=%d output_tokens=%d stop_reason=%s resp=%s",
				a.providerName, upstreamModel, lookup.Chain.ResponseID, len(lookup.Tail), len(req.Messages), cachedTokens, inputTokens, outputTokens, stopReason, newResponseID)
		} else {
			if cachedTokens > 0 {
				a.store.Stats.Hits.Add(1)
			} else {
				a.store.Stats.Misses.Add(1)
			}
			reason := "full replay"
			if forceFull {
				reason = "forced full replay"
			} else if !a.pc.ExperimentalPreviousResponseID {
				reason = "codex-compatible http replay"
			} else if lookup.FullReplayReason != "" {
				reason = lookup.FullReplayReason
			}
			promptCache := "miss"
			if cachedTokens > 0 {
				promptCache = "hit"
			}
			log.Printf("responses: full_replay provider=%s model=%s reason=%q prompt_cache=%s cached_tokens=%d input_tokens=%d output_tokens=%d stop_reason=%s resp=%s",
				a.providerName, upstreamModel, reason, promptCache, cachedTokens, inputTokens, outputTokens, stopReason, newResponseID)
		}
	}
	b.Finish()
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

func isInvalidPreviousResponseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "previous_response_id") ||
		strings.Contains(msg, "previous response") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "expired")
}

func isRetriableUpstreamMessage(msg string) bool {
	msg = strings.ToLower(msg)
	for _, marker := range []string{"429", "500", "502", "503", "504", "busy", "overload", "rate limit", "timeout", "timed out", "temporary", "try again", "unavailable"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func streamRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(attempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isCallableItem(kind string) bool {
	return kind == "function_call" || kind == "custom_tool_call"
}

func normalizeToolArgsForAnthropic(kind, args string) string {
	if kind != "custom_tool_call" {
		if args == "" {
			return "{}"
		}
		return args
	}
	trimmed := strings.TrimSpace(args)
	if trimmed != "" && strings.HasPrefix(trimmed, "{") {
		var value map[string]any
		if json.Unmarshal([]byte(trimmed), &value) == nil {
			return trimmed
		}
	}
	buf, err := json.Marshal(map[string]string{"input": args})
	if err != nil {
		return `{"input":""}`
	}
	return string(buf)
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
	Input   string             `json:"input,omitempty"`
	Output  string             `json:"output,omitempty"`
}

type inputContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type toolDecl struct {
	Type              string          `json:"type"`
	Name              string          `json:"name,omitempty"`
	Description       string          `json:"description,omitempty"`
	Strict            *bool           `json:"strict,omitempty"`
	Parameters        json.RawMessage `json:"parameters,omitempty"`
	Format            map[string]any  `json:"format,omitempty"`
	ExternalWebAccess *bool           `json:"external_web_access,omitempty"`
}

func buildRequest(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, error) {
	body, _, err := buildRequestWithAliases(r, model, passthroughServerTools)
	return body, err
}

func buildRequestWithAliases(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, map[string]map[string]string, error) {
	return buildRequestWithOptions(r, model, passthroughServerTools, false, "")
}

func buildRequestWithOptions(r *anthropic.Request, model string, passthroughServerTools, compactToolResults bool, customToolMode string) (map[string]any, map[string]map[string]string, error) {
	return buildRequestWithNormalizer(r, model, passthroughServerTools, compactToolResults, responsesCustomToolMode(customToolMode), anthropic.NormalizeMessagesForUpstream)
}

func buildTailRequestWithAliases(r *anthropic.Request, model string, passthroughServerTools bool) (map[string]any, map[string]map[string]string, error) {
	return buildTailRequestWithOptions(r, model, passthroughServerTools, false, "")
}

func buildTailRequestWithOptions(r *anthropic.Request, model string, passthroughServerTools, compactToolResults bool, customToolMode string) (map[string]any, map[string]map[string]string, error) {
	return buildRequestWithNormalizer(r, model, passthroughServerTools, compactToolResults, responsesCustomToolMode(customToolMode), anthropic.NormalizePreviousResponseTail)
}

func buildRequestWithNormalizer(
	r *anthropic.Request,
	model string,
	passthroughServerTools bool,
	compactToolResults bool,
	customToolMode string,
	normalize func([]anthropic.Message, bool, bool) []anthropic.Message,
) (map[string]any, map[string]map[string]string, error) {
	if fields := r.UnsupportedOpenAIFields(); len(fields) > 0 {
		return nil, nil, fmt.Errorf("openai_responses does not support Anthropic-only fields: %s", strings.Join(fields, ", "))
	}
	items, err := convertMessagesToItems(
		normalize(r.Messages, passthroughServerTools, r.HasToolSearchBeta()),
		passthroughServerTools,
		responsesCustomToolNames(r.Tools, passthroughServerTools, customToolMode),
		responsesFunctionModeCustomToolNames(r.Tools, passthroughServerTools, customToolMode),
		compactToolResults,
	)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"model":  model,
		"input":  items,
		"stream": true,
	}
	sysText, err := anthropic.SystemText(r.System)
	if err != nil {
		return nil, nil, err
	}
	if sysText = responsesInstructions(sysText, len(anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)) > 0 && !isToolChoiceNone(r.ToolChoice)); sysText != "" {
		body["instructions"] = sysText
	}
	aliases, err := applyCommonFields(body, r, passthroughServerTools, customToolMode)
	if err != nil {
		return nil, nil, err
	}
	return body, aliases, nil
}

const toolUseBridgeInstruction = provider.ToolUseBridgeInstruction

func responsesInstructions(sysText string, hasTools bool) string {
	return provider.InstructionsWithToolUseBridge(sysText, hasTools)
}

func applyCommonFields(body map[string]any, r *anthropic.Request, passthroughServerTools bool, customToolMode string) (map[string]map[string]string, error) {
	aliases := map[string]map[string]string{}
	if r.MaxTokens > 0 {
		body["max_output_tokens"] = r.MaxTokens
	}
	if r.TopP != nil {
		body["top_p"] = *r.TopP
	}
	if effort, ok := r.ReasoningEffort(); ok {
		body["reasoning"] = map[string]any{"effort": effort}
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	text, err := responsesTextFormat(r)
	if err != nil {
		return nil, err
	}
	if text != nil {
		body["text"] = text
	}
	// Match openai/codex HTTP request shape: tool_choice and parallel_tool_calls
	// are always present; keep parallel calls disabled for Claude Code tools.
	body["parallel_tool_calls"] = false
	if _, set := body["store"]; !set {
		body["store"] = false
	}
	toolKinds := map[string]string{}
	if len(r.Tools) > 0 {
		upstreamTools := anthropic.ToolsForUpstream(r.Tools, passthroughServerTools)
		if len(upstreamTools) > 0 {
			tools := make([]toolDecl, 0, len(upstreamTools))
			for _, t := range upstreamTools {
				kind := responsesToolKind(t, customToolMode)
				toolKinds[t.Name] = kind
				params, toolAliases := toolargs.SanitizeParameters(t.InputSchema)
				if len(toolAliases) > 0 {
					aliases[t.Name] = toolAliases
				}
				tools = append(tools, toResponsesToolDecl(t, kind, params, customToolMode))
			}
			body["tools"] = tools
		}
	}
	body["tool_choice"] = responsesToolChoice(r.ToolChoice, toolKinds)
	return aliases, nil
}

func responsesCustomToolMode(mode string) string {
	if mode == "function" {
		return "function"
	}
	return "native"
}

func responsesToolKind(t anthropic.Tool, customToolMode string) string {
	if t.Type == "custom" && len(t.InputSchema) == 0 && customToolMode != "function" {
		return "custom"
	}
	return "function"
}

func toResponsesToolDecl(t anthropic.Tool, kind string, params json.RawMessage, customToolMode string) toolDecl {
	if kind == "custom" {
		return toolDecl{
			Type:        "custom",
			Name:        t.Name,
			Description: t.Description,
			Format:      map[string]any{"type": "text"},
		}
	}
	strict := false
	if t.Strict != nil {
		strict = *t.Strict
	}
	if t.Type == "custom" && len(t.InputSchema) == 0 && customToolMode == "function" {
		strict = true
		params = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`)
	}
	return toolDecl{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Strict:      &strict,
		Parameters:  params,
	}
}

func customToolInput(args string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err == nil {
		if input, ok := obj["input"].(string); ok && len(obj) == 1 {
			return input
		}
	}
	return args
}

func functionModeCustomArgs(args string) string {
	raw := customToolInput(args)
	out, err := json.Marshal(map[string]string{"input": raw})
	if err != nil {
		return `{"input":""}`
	}
	return string(out)
}

func isToolChoiceNone(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var choice struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return false
	}
	return choice.Type == "none"
}

func responsesToolChoice(raw json.RawMessage, toolKinds map[string]string) any {
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
			kind := toolKinds[choice.Name]
			if kind == "" {
				kind = "function"
			}
			return map[string]any{"type": kind, "name": choice.Name}
		}
	}
	return "auto"
}

func responsesTextFormat(r *anthropic.Request) (any, error) {
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
		if _, ok := f["schema"]; !ok {
			return nil, fmt.Errorf("output_config.format.schema is required for json_schema")
		}
		if _, ok := f["strict"]; !ok {
			f["strict"] = true
		}
		if name, _ := f["name"].(string); name == "" {
			f["name"] = "relaycode_output_schema"
		}
	case "text", "json_object":
	default:
		return nil, fmt.Errorf("unsupported output_config.format.type %q for openai_responses", typ)
	}
	return map[string]any{"format": f}, nil
}

func responsesCustomToolNames(tools []anthropic.Tool, passthroughServerTools bool, customToolMode string) map[string]bool {
	out := map[string]bool{}
	for _, tool := range anthropic.ToolsForUpstream(tools, passthroughServerTools) {
		if responsesToolKind(tool, customToolMode) == "custom" && tool.Name != "" {
			out[tool.Name] = true
		}
	}
	return out
}

func responsesFunctionModeCustomToolNames(tools []anthropic.Tool, passthroughServerTools bool, customToolMode string) map[string]bool {
	out := map[string]bool{}
	if customToolMode != "function" {
		return out
	}
	for _, tool := range anthropic.ToolsForUpstream(tools, passthroughServerTools) {
		if tool.Type == "custom" && len(tool.InputSchema) == 0 && tool.Name != "" {
			out[tool.Name] = true
		}
	}
	return out
}

func convertMessagesToItems(msgs []anthropic.Message, passthroughServerTools bool, customToolNames, functionCustomToolNames map[string]bool, compactToolResults bool) ([]inputItem, error) {
	var out []inputItem
	callKinds := map[string]string{}
	for _, m := range msgs {
		blocks := anthropic.BlocksForUpstream(m.Content.AsBlocks(), passthroughServerTools)
		switch m.Role {
		case "user":
			msg := inputItem{Type: "message", Role: "user"}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					msg.Content = append(msg.Content, inputContentPart{Type: "input_text", Text: b.Text})
				case "image":
					imageURL, err := b.ImageDataURL()
					if err != nil {
						return nil, fmt.Errorf("image user block: %w", err)
					}
					msg.Content = append(msg.Content, inputContentPart{Type: "input_image", ImageURL: imageURL})
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
					text, err := anthropic.ToolResultTextForUpstream(b.Content, compactToolResults)
					if err != nil {
						return nil, err
					}
					itemType := "function_call_output"
					if callKinds[b.ToolUseID] == "custom" {
						itemType = "custom_tool_call_output"
					}
					out = append(out, inputItem{
						Type:   itemType,
						CallID: b.ToolUseID,
						Output: text,
					})
				}
			}
			if len(msg.Content) > 0 {
				out = append(out, msg)
			}
		case "assistant":
			var preText []string
			var postText []string
			var pendingCalls []inputItem
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
					// drop replay: Responses API does not accept raw thinking blocks
				case "tool_use", "server_tool_use", "mcp_tool_use":
					if b.Type == "server_tool_use" && b.Name == "web_search" {
						continue
					}
					if b.ID == "" || b.Name == "" {
						continue
					}
					seenTool = true
					args := "{}"
					if len(b.Input) > 0 {
						args = string(b.Input)
					}
					item := inputItem{Type: "function_call", Name: b.Name, CallID: b.ID, Args: args}
					if customToolNames[b.Name] {
						item.Type = "custom_tool_call"
						item.Args = ""
						item.Input = customToolInput(args)
						callKinds[b.ID] = "custom"
					} else {
						if functionCustomToolNames[b.Name] {
							item.Args = functionModeCustomArgs(args)
						}
						callKinds[b.ID] = "function"
					}
					pendingCalls = append(pendingCalls, item)
				}
			}
			if text := strings.TrimSpace(strings.Join(preText, "\n\n")); text != "" {
				out = append(out, inputItem{
					Type: "message",
					Role: "assistant",
					Content: []inputContentPart{
						{Type: "output_text", Text: text},
					},
				})
			}
			out = append(out, pendingCalls...)
			if text := strings.TrimSpace(strings.Join(postText, "\n\n")); text != "" {
				out = append(out, inputItem{
					Type: "message",
					Role: "assistant",
					Content: []inputContentPart{
						{Type: "output_text", Text: text},
					},
				})
			}
		default:
			return nil, fmt.Errorf("unsupported role %q", m.Role)
		}
	}
	return out, nil
}
