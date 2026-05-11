// Package session maintains an in-memory map of upstream Responses API
// sessions so repeat Claude Code turns can chain via previous_response_id
// instead of replaying the full conversation.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5nYqnHvk/RelayCode/internal/anthropic"
)

// Entry is one cached session snapshot.
type Entry struct {
	Key              string // fingerprint across all messages[:MessageCount]
	Provider         string // cfg provider name
	UpstreamModel    string // upstream model id
	ToolsHash        string // tools fingerprint
	InstructionsHash string // instructions fingerprint
	MessageCount     int    // messages[:MessageCount] are covered by this session
	ResponseID       string // upstream response.id to chain from
	LastUsed         time.Time
	OutputTokens     int // cumulative output tokens for logging
	InputTokens      int
}

// Lookup is the result of attempting to reuse a prior session.
type Lookup struct {
	// Chain, when non-nil, points at the session whose ResponseID should be
	// sent as previous_response_id. Tail is the slice of messages to send as
	// the new input.
	Chain *Entry
	Tail  []anthropic.Message

	// FullReplayReason explains why chaining was declined (empty if chained).
	FullReplayReason string

	// Always set: a fingerprint covering the full message list. Used to key
	// the new session entry once the upstream call succeeds.
	NewKey           string
	ToolsHash        string
	InstructionsHash string
}

// Stats are cumulative, atomic-safe counters for /debug/stats.
type Stats struct {
	Hits           atomic.Int64
	Misses         atomic.Int64
	ForcedReplays  atomic.Int64
	ExpiredInvalid atomic.Int64
	InputTokens    atomic.Int64
	OutputTokens   atomic.Int64
}

// Store is a small thread-safe cache keyed by the per-prefix fingerprint.
type Store struct {
	mu      sync.Mutex
	entries map[string]*Entry
	ttl     time.Duration
	max     int
	Stats   Stats
}

func NewStore(ttl time.Duration, max int) *Store {
	if max <= 0 {
		max = 1000
	}
	return &Store{
		entries: map[string]*Entry{},
		ttl:     ttl,
		max:     max,
	}
}

// Prepare inspects an incoming request against existing sessions and returns
// a Lookup describing whether to chain or full-replay.
func (s *Store) Prepare(provider, upstreamModel string, req *anthropic.Request) (*Lookup, error) {
	instr, err := anthropic.SystemText(req.System)
	if err != nil {
		return nil, err
	}
	instrHash := hashString(instr)
	toolsHash, err := hashTools(req.Tools)
	if err != nil {
		return nil, err
	}

	prefixHashes, err := hashMessagePrefixes(req.Messages)
	if err != nil {
		return nil, err
	}
	// full-length key
	fullKey := combineKey(provider, upstreamModel, instrHash, toolsHash, prefixHashes[len(prefixHashes)-1])

	lookup := &Lookup{
		NewKey:           fullKey,
		ToolsHash:        toolsHash,
		InstructionsHash: instrHash,
	}

	// Only chainable if tail is exclusively user-role messages (see package doc).
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	// Session matching: a stored session with message_count=k means the
	// upstream response covers messages[:k] plus its own assistant reply.
	// On the next client turn, the client will replay:
	//   messages[:k]      = the prefix the session already contains
	//   messages[k..j-1]  = one or more assistant/tool_use blocks the model
	//                       emitted during that response (which upstream still
	//                       remembers via previous_response_id)
	//   messages[j..N-1]  = new user input (text and/or tool_results)
	// We only chain if the "skip" span messages[k..j-1] contains no user
	// messages and the "tail" messages[j..] is purely user-role.
	for k := len(req.Messages) - 1; k >= 1; k-- {
		key := combineKey(provider, upstreamModel, instrHash, toolsHash, prefixHashes[k-1])
		e, ok := s.entries[key]
		if !ok {
			continue
		}
		if e.Provider != provider || e.UpstreamModel != upstreamModel {
			continue
		}
		if e.ToolsHash != toolsHash || e.InstructionsHash != instrHash {
			continue
		}
		// k messages already in the session. The replayed assistant turn is
		// messages[k..j-1]; find the first user index >= k.
		j := k
		validSkip := true
		for j < len(req.Messages) && req.Messages[j].Role != "user" {
			if req.Messages[j].Role != "assistant" {
				validSkip = false
				break
			}
			j++
		}
		if !validSkip || j >= len(req.Messages) {
			lookup.FullReplayReason = "no new user tail after stored prefix"
			continue
		}
		tail := req.Messages[j:]
		if !chainableTail(tail) {
			lookup.FullReplayReason = "tail contains non-user messages"
			continue
		}
		lookup.Chain = e
		lookup.Tail = tail
		e.LastUsed = time.Now()
		return lookup, nil
	}
	if lookup.FullReplayReason == "" {
		lookup.FullReplayReason = "no matching prefix"
	}
	return lookup, nil
}

// Commit stores (or refreshes) a session entry keyed by the full-length
// fingerprint returned from Prepare.
func (s *Store) Commit(lookup *Lookup, provider, upstreamModel string, messageCount int, responseID string, inputTokens, outputTokens int) {
	if responseID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.entries) >= s.max {
		s.evictLRULocked()
	}
	s.entries[lookup.NewKey] = &Entry{
		Key:              lookup.NewKey,
		Provider:         provider,
		UpstreamModel:    upstreamModel,
		ToolsHash:        lookup.ToolsHash,
		InstructionsHash: lookup.InstructionsHash,
		MessageCount:     messageCount,
		ResponseID:       responseID,
		LastUsed:         time.Now(),
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
	}
}

// Invalidate drops a known-bad entry (e.g. upstream returned 404 for its
// response_id). Safe to call with empty key.
func (s *Store) Invalidate(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Snapshot returns a shallow copy for /debug/stats.
func (s *Store) Snapshot() map[string]Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Entry, len(s.entries))
	for k, e := range s.entries {
		out[k] = *e
	}
	return out
}

// ---- internals ----

func (s *Store) pruneLocked() {
	if s.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.ttl)
	for k, e := range s.entries {
		if e.LastUsed.Before(cutoff) {
			delete(s.entries, k)
		}
	}
}

func (s *Store) evictLRULocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, e := range s.entries {
		if first || e.LastUsed.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.LastUsed
			first = false
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

func chainableTail(tail []anthropic.Message) bool {
	if len(tail) == 0 {
		return false
	}
	for _, m := range tail {
		if m.Role != "user" {
			return false
		}
	}
	return true
}

// hashMessagePrefixes returns a slice where index i = hash of messages[:i+1].
// The hash is order-sensitive, content-stable, independent of JSON field order.
func hashMessagePrefixes(msgs []anthropic.Message) ([]string, error) {
	h := sha256.New()
	out := make([]string, len(msgs))
	for i, m := range msgs {
		norm, err := normalizeMessage(m)
		if err != nil {
			return nil, err
		}
		h.Write([]byte{0x01})
		h.Write(norm)
		out[i] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}

func normalizeMessage(m anthropic.Message) ([]byte, error) {
	blocks := m.Content.AsBlocks()
	// Marshal a stable structure: role + ordered blocks.
	buf := map[string]any{"role": m.Role}
	if len(blocks) == 0 {
		buf["content"] = []any{}
	} else {
		items := make([]any, 0, len(blocks))
		for _, b := range blocks {
			items = append(items, normalizeBlock(b))
		}
		buf["content"] = items
	}
	return canonicalJSON(buf)
}

func normalizeBlock(b anthropic.Block) map[string]any {
	m := map[string]any{"type": b.Type}
	if b.Text != "" {
		m["text"] = b.Text
	}
	if b.Thinking != "" {
		m["thinking"] = b.Thinking
	}
	if b.Signature != "" {
		m["signature"] = b.Signature
	}
	if b.ID != "" {
		m["id"] = b.ID
	}
	if b.Name != "" {
		m["name"] = b.Name
	}
	if len(b.Input) > 0 {
		m["input"] = json.RawMessage(b.Input)
	}
	if b.ToolUseID != "" {
		m["tool_use_id"] = b.ToolUseID
	}
	if len(b.Content) > 0 {
		m["content"] = json.RawMessage(b.Content)
	}
	if b.IsError {
		m["is_error"] = true
	}
	return m
}

func hashTools(tools []anthropic.Tool) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	items := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		items = append(items, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": json.RawMessage(t.InputSchema),
		})
	}
	buf, err := canonicalJSON(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

func hashString(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func combineKey(provider, model, instrHash, toolsHash, prefixHash string) string {
	h := sha256.New()
	h.Write([]byte(provider))
	h.Write([]byte{0x1f})
	h.Write([]byte(model))
	h.Write([]byte{0x1f})
	h.Write([]byte(instrHash))
	h.Write([]byte{0x1f})
	h.Write([]byte(toolsHash))
	h.Write([]byte{0x1f})
	h.Write([]byte(prefixHash))
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON marshals a value with sorted map keys at every level so the
// resulting byte sequence depends only on content, not input ordering.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return canonicalMarshal(parsed), nil
}

func canonicalMarshal(v any) []byte {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kb, _ := json.Marshal(k)
			buf = append(buf, kb...)
			buf = append(buf, ':')
			buf = append(buf, canonicalMarshal(t[k])...)
		}
		buf = append(buf, '}')
		return buf
	case []any:
		buf := []byte{'['}
		for i, item := range t {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, canonicalMarshal(item)...)
		}
		buf = append(buf, ']')
		return buf
	default:
		out, _ := json.Marshal(t)
		return out
	}
}
