// Package sse emits Anthropic-format server-sent events.
package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Writer is a low-level helper that streams SSE events to an http.ResponseWriter
// and flushes after each event. Safe for single-goroutine use.
type Writer struct {
	mu  sync.Mutex
	w   http.ResponseWriter
	fl  http.Flusher
	err error
}

func NewWriter(w http.ResponseWriter) *Writer {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	return &Writer{w: w, fl: fl}
}

func (w *Writer) Err() error { return w.err }

func (w *Writer) WriteRaw(line string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	_, err := io.WriteString(w.w, line)
	if err != nil {
		w.err = err
		return err
	}
	if w.fl != nil {
		w.fl.Flush()
	}
	return nil
}

func (w *Writer) Event(event string, data any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		w.err = err
		return
	}
	if _, err := fmt.Fprintf(w.w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		w.err = err
		return
	}
	if w.fl != nil {
		w.fl.Flush()
	}
}

// ---- Anthropic message lifecycle ----

// Builder tracks content block indices while streaming an Anthropic message.
type Builder struct {
	w         *Writer
	messageID string
	model     string

	inputTokens  int
	outputTokens int

	started       bool
	nextIndex     int
	textIndex     int
	thinkingIndex int
	textOpen      bool
	thinkingOpen  bool

	tools      map[string]*toolState // keyed by upstream call id
	toolsOrder []*toolState

	serverToolUse  map[string]int
	stopReason     string
	errorMessage   string
	emittedContent bool
	finished       bool
}

type toolState struct {
	blockIndex int
	id         string
	name       string
	started    bool
}

func NewBuilder(w *Writer, messageID, model string, inputTokens int) *Builder {
	return &Builder{
		w:             w,
		messageID:     messageID,
		model:         model,
		inputTokens:   inputTokens,
		textIndex:     -1,
		thinkingIndex: -1,
		tools:         map[string]*toolState{},
	}
}

func (b *Builder) Start() {
	if b.started {
		return
	}
	b.started = true
	b.w.Event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            b.messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         b.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  b.inputTokens,
				"output_tokens": 1,
			},
		},
	})
}

func (b *Builder) EnsureText() {
	if b.textOpen {
		return
	}
	b.emittedContent = true
	b.closeThinking()
	b.textIndex = b.alloc()
	b.textOpen = true
	b.w.Event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         b.textIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (b *Builder) EmitText(chunk string) {
	if chunk == "" {
		return
	}
	b.EnsureText()
	b.w.Event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": b.textIndex,
		"delta": map[string]any{"type": "text_delta", "text": chunk},
	})
}

func (b *Builder) EnsureThinking() {
	if b.thinkingOpen {
		return
	}
	b.emittedContent = true
	b.closeText()
	b.thinkingIndex = b.alloc()
	b.thinkingOpen = true
	b.w.Event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         b.thinkingIndex,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
}

func (b *Builder) EmitThinking(chunk string) {
	if chunk == "" {
		return
	}
	b.EnsureThinking()
	b.w.Event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": b.thinkingIndex,
		"delta": map[string]any{"type": "thinking_delta", "thinking": chunk},
	})
}

func (b *Builder) StartTool(callID, name string) {
	b.startToolBlock(callID, name, "tool_use")
}

func (b *Builder) StartServerTool(callID, name string) {
	b.StartServerToolWithInput(callID, name, map[string]any{})
}

func (b *Builder) StartServerToolWithInput(callID, name string, input any) {
	b.startToolBlockWithInput(callID, name, "server_tool_use", input)
}

func (b *Builder) startToolBlock(callID, name, typ string) {
	b.startToolBlockWithInput(callID, name, typ, map[string]any{})
}

func (b *Builder) startToolBlockWithInput(callID, name, typ string, input any) {
	b.emittedContent = true
	b.closeText()
	b.closeThinking()
	ts, ok := b.tools[callID]
	if !ok {
		ts = &toolState{id: callID, name: name, blockIndex: b.alloc()}
		b.tools[callID] = ts
		b.toolsOrder = append(b.toolsOrder, ts)
	} else if ts.name == "" && name != "" {
		ts.name = name
	}
	if ts.started {
		return
	}
	ts.started = true
	b.w.Event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": ts.blockIndex,
		"content_block": map[string]any{
			"type":  typ,
			"id":    ts.id,
			"name":  ts.name,
			"input": input,
		},
	})
}

func (b *Builder) EmitToolInput(callID, partialJSON string) {
	ts, ok := b.tools[callID]
	if !ok || !ts.started {
		return
	}
	if partialJSON == "" {
		return
	}
	b.w.Event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": ts.blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	})
}

func (b *Builder) StopTool(callID string) {
	ts, ok := b.tools[callID]
	if !ok || !ts.started {
		return
	}
	ts.started = false
	b.w.Event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": ts.blockIndex,
	})
}

func (b *Builder) EmitWebSearchResult(callID string, content any) {
	b.EmitServerToolResult("web_search_tool_result", callID, content)
}

func (b *Builder) EmitServerToolResult(resultType, callID string, content any) {
	b.emittedContent = true
	b.closeText()
	b.closeThinking()
	b.closeAllTools()
	index := b.alloc()
	b.w.Event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type":        resultType,
			"tool_use_id": callID,
			"content":     content,
		},
	})
	b.w.Event("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

func (b *Builder) AddServerToolUse(key string, count int) {
	if key == "" || count == 0 {
		return
	}
	if b.serverToolUse == nil {
		b.serverToolUse = map[string]int{}
	}
	b.serverToolUse[key] += count
}

func (b *Builder) SetStopReason(r string)    { b.stopReason = r }
func (b *Builder) SetOutputTokens(n int)     { b.outputTokens = n }
func (b *Builder) AddOutputTokens(delta int) { b.outputTokens += delta }
func (b *Builder) AddInputTokens(delta int)  { b.inputTokens += delta }
func (b *Builder) Finished() bool            { return b.finished }
func (b *Builder) ErrorMessage() string      { return b.errorMessage }
func (b *Builder) HasContent() bool          { return b.emittedContent }
func (b *Builder) MarkFinished()             { b.finished = true }
func (b *Builder) RawWriter() *Writer        { return b.w }

func (b *Builder) closeText() {
	if !b.textOpen {
		return
	}
	b.textOpen = false
	b.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": b.textIndex,
	})
}

func (b *Builder) closeThinking() {
	if !b.thinkingOpen {
		return
	}
	b.thinkingOpen = false
	b.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": b.thinkingIndex,
	})
}

func (b *Builder) closeAllTools() {
	for _, ts := range b.toolsOrder {
		if ts.started {
			b.StopTool(ts.id)
		}
	}
}

func (b *Builder) Finish() {
	if b.finished {
		return
	}
	b.finished = true
	b.closeText()
	b.closeThinking()
	b.closeAllTools()
	reason := b.stopReason
	if reason == "" {
		reason = "end_turn"
	}
	usage := map[string]any{
		"input_tokens":  b.inputTokens,
		"output_tokens": b.outputTokens,
	}
	if len(b.serverToolUse) > 0 {
		usage["server_tool_use"] = b.serverToolUse
	}
	b.w.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
		"usage": usage,
	})
	b.w.Event("message_stop", map[string]any{"type": "message_stop"})
}

// FinishWithError closes any open blocks and emits a valid Anthropic SSE error.
func (b *Builder) FinishWithError(msg string) {
	if b.finished {
		return
	}
	b.errorMessage = msg
	if !b.started {
		b.w.Event("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": msg},
		})
		b.finished = true
		return
	}
	b.EmitText(msg)
	b.outputTokens += EstimateOutputTokens(msg)
	b.stopReason = "end_turn"
	b.Finish()
}

func (b *Builder) alloc() int {
	i := b.nextIndex
	b.nextIndex++
	return i
}

// EstimateOutputTokens is a cheap char/4 approximation when upstream doesn't
// provide usage. Callers can override with SetOutputTokens.
func EstimateOutputTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len(text) / 4
	if n < 1 {
		return 1
	}
	return n
}

var _ io.Writer = (*nopWriter)(nil)

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
