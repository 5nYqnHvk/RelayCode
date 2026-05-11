# RelayCode

<p align="center">
  <strong>A single-binary proxy that lets Claude Code drive OpenAI-compatible backends.</strong>
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#routing">Routing</a> ·
  <a href="#token-optimization">Token optimization</a> ·
  <a href="#observability">Observability</a>
</p>

---

RelayCode sits between the **Claude Code** CLI and a pool of OpenAI-compatible
backends. It speaks Anthropic's Messages API on the client side and translates
each request into either the OpenAI **Chat Completions** or **Responses**
protocol on the server side, streaming the reply back as a faithful Anthropic
SSE event sequence.

The result: you keep the Claude Code UX and point it at GPT-5.x on the OpenAI
Responses API, or at DeepSeek / Kimi-style chat endpoints, with upstream
prompt caching chained correctly so follow-up turns cost a fraction of a
full replay.

## Highlights

- **Single static binary.** ~10 MB, Go stdlib only, no runtime deps.
- **Two egress protocols.** `openai_chat` (`/v1/chat/completions`) and
  `openai_responses` (`/v1/responses`).
- **Model-aware routing.** Match incoming Claude model names (`opus`,
  `sonnet`, `haiku`, `*`) to different backends and upstream model ids.
- **Correct streaming.** Full Anthropic SSE lifecycle (message_start →
  content_block_* → message_delta → message_stop) with tool_use,
  input_json_delta, thinking, stop_reason mapping.
- **Upstream cache-friendly.** Mirrors the `openai/codex` CLI request shape
  and keys `prompt_cache_key` off Claude Code's conversation `session_id`
  so multi-turn conversations reuse ~98% of the prefix.
- **Function-call scaffolding scrub.** Drops gateway-leaked
  `<function_calls>` tags without touching real JSX / template brackets.
- **`/debug/stats` endpoint.** Live cache hit/miss counters plus per-session
  table, protected by the same auth token as `/v1/messages`.

## Quickstart

```bash
git clone https://github.com/5nYqnHvk/RelayCode.git
cd RelayCode
go build -o relaycode ./cmd/relaycode

cp relaycode.example.yaml relaycode.yaml
# edit relaycode.yaml: set your provider API keys (or use ${OPENAI_API_KEY} env)
export OPENAI_API_KEY=sk-...
./relaycode -config relaycode.yaml
```

Point Claude Code at the proxy:

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_AUTH_TOKEN=freecc   # match server.auth_token in yaml
claude
```

That's it. Every `claude` turn now streams through RelayCode to whichever
backend the router picked.

## How it works

```
┌──────────────┐   Anthropic        ┌────────────────┐   OpenAI         ┌──────────────┐
│ Claude Code  │ ─── /v1/messages ─▶│   RelayCode    │──/v1/responses──▶│  OpenAI /    │
│   (client)   │ ◀─── SSE stream ───│                │  /chat/...       │  DeepSeek /  │
└──────────────┘                    └────────────────┘                  │  Kimi / ...  │
                                                                        └──────────────┘
```

Per request, RelayCode:

1. Decodes the Anthropic Messages body (text, `tool_use`, `tool_result`,
   `thinking`, cache_control markers, metadata).
2. Picks a route based on the incoming model name.
3. Translates to the chosen egress protocol: Chat Completions keeps
   `messages[]`; Responses emits `input[]` items (`message`,
   `function_call`, `function_call_output`).
4. Streams the upstream reply, translating deltas back into Anthropic SSE
   blocks in real time — text → `text_delta`, reasoning →
   `thinking_delta`, tool arguments → `input_json_delta`.
5. On completion, maps stop reasons and emits `message_delta` + `message_stop`.

## Architecture

```
go/
├── cmd/relaycode/          # entrypoint, flag parsing, signal shutdown
├── internal/
│   ├── config/             # yaml loader (stdlib-only parser) + types
│   ├── anthropic/          # ingress types for /v1/messages
│   ├── sse/                # anthropic SSE Writer + Builder
│   ├── router/             # model-name → provider + upstream model
│   ├── provider/
│   │   ├── http.go         # PostStream + SSE line reader
│   │   ├── provider.go     # Adapter + SessionAware interfaces
│   │   ├── chat/           # OpenAI chat completions egress
│   │   └── responses/      # OpenAI responses egress + tag scrubber
│   ├── session/            # in-memory cache key store + stats counters
│   └── server/             # http ingress, /v1/messages, /debug/stats
├── go.mod
├── relaycode.example.yaml
└── README.md
```

Dependency direction (top → bottom):

```
   server
      │
      ├── router ──────────┐
      │                    │
      ├── provider.chat ───┤
      │                    │
      └── provider.responses
             │          │
             │          ├── session
             │          └── sse
             │
             └── anthropic ── config
```

**Design rules**

- Adapters implement a single `Stream(ctx, req, upstreamModel, builder)`
  method and push all output through `sse.Builder`. They never write to
  `http.ResponseWriter` directly.
- `SessionAware` is optional. Adapters that care about cache keying (today:
  Responses) implement it; others run stateless.
- All upstream JSON shapes are produced with `map[string]any` or narrow
  structs defined inside the adapter — no shared "one big DTO" type.
- `core.anthropic` has zero dependencies on `provider.*` so it can be
  reused from unit tests without pulling adapter code.

## Configuration

`relaycode.example.yaml`:

```yaml
server:
  host: 127.0.0.1
  port: 8080
  auth_token: "freecc"   # clients must send matching x-api-key or Bearer token

routes:
  - match: "opus"
    provider: openai_responses
    model: gpt-5.5
  - match: "sonnet"
    provider: openai_responses
    model: gpt-5.4
  - match: "*"                 # fallback: required
    provider: deepseek_chat
    model: deepseek-chat

providers:
  openai_responses:
    kind: openai_responses     # POST /v1/responses
    base_url: https://api.openai.com/v1
    api_key: "${OPENAI_API_KEY}"

  openai_chat:
    kind: openai_chat          # POST /v1/chat/completions
    base_url: https://api.openai.com/v1
    api_key: "${OPENAI_API_KEY}"

  deepseek_chat:
    kind: openai_chat
    base_url: https://api.deepseek.com/v1
    api_key: "${DEEPSEEK_API_KEY}"
```

**Config rules**

- `${VAR}` is expanded from the process env at load time.
- `routes[].match` is a case-insensitive substring of the incoming model
  name. First match wins. A fallback `"*"` entry is required.
- `providers[].kind` must be `openai_chat` or `openai_responses`.
- Unknown provider kinds are rejected at startup.
- Missing API keys are reported lazily on the first request that routes to
  that provider (so a proxy with only one upstream configured doesn't fail
  to boot just because an unused key is unset).

## Routing

Claude Code ships three model tiers:

| Claude model                  | Example `match` | Typical route  |
|-------------------------------|-----------------|----------------|
| `claude-opus-4-7`             | `opus`          | strongest GPT  |
| `claude-sonnet-4-6`           | `sonnet`        | mid tier       |
| `claude-haiku-4-5-20251001`   | `haiku`         | cheap / codex  |

Any request that doesn't hit an explicit `match` falls through to the `"*"`
route. This lets you split large/complex turns to OpenAI Responses while
routing small bash / title-generation turns to a cheaper chat-completions
backend.

## Token optimization

The Responses adapter mirrors openai/codex's HTTP shape so upstream prompt
caching lights up correctly:

- `tool_choice: "auto"` and `parallel_tool_calls: true` are always present.
- `store: false` (matches codex on openai.com).
- `prompt_cache_key` is set to the **Claude Code conversation session id**,
  extracted from `metadata.user_id` (`{..., "session_id": "..."}`). This is
  the same convention codex uses (`thread_id`) and keeps every follow-up
  turn routed to the same server-side cache entry.
- The volatile `x-anthropic-billing-header` block Claude Code injects into
  `system` is stripped before building `instructions`, because its rotating
  `cch=<hash>` field would otherwise change the prefix byte-for-byte every
  turn and blow the cache.

Measured against an OpenAI Responses backend on a real Claude Code session
(`claude-opus-4-7`, single conversation, four user turns in a row):

| Turn | `cached_tokens` | `input_tokens` | notes                         |
|------|-----------------|----------------|-------------------------------|
| 1    | 23,040          | 52,854         | first turn, cold cache         |
| 2    | 75,264          | 915            | full history in cache          |
| 3    | 75,264          | 1,119          | +204 new tokens                |
| 4    | 75,264          | 1,332          | +213 new tokens                |

Turns 2+ spend ~98% fewer billable input tokens than a naive full replay.

## Observability

```bash
curl -sS http://127.0.0.1:8080/debug/stats \
     -H "x-api-key: freecc" | jq .
```

```json
{
  "counters": {
    "hits": 6,
    "misses": 1,
    "forced_replays": 0,
    "expired_invalid": 0,
    "input_tokens": 59895,
    "output_tokens": 936
  },
  "sessions": [
    {
      "provider": "openai_responses",
      "upstream_model": "gpt-5.5",
      "message_count": 5,
      "response_id": "resp_...",
      "last_used": "2026-05-11T05:32:50Z",
      "input_tokens": 1119,
      "output_tokens": 30
    }
  ]
}
```

Set `RELAYCODE_DEBUG_REQUEST=1` to dump raw `/v1/messages` bodies to stderr
when diagnosing upstream issues. Off by default so prompts never leak.

## Tool compatibility

RelayCode proxies a non-Anthropic backend, so Anthropic-hosted tools do
not work through it. The table below lists **only constraints the proxy
itself imposes** — things unrelated to the proxy (client auth, sandboxing,
dev-harness integration) are out of scope here.

| Claude Code tool | Through RelayCode | Notes |
|---|---|---|
| `Bash`, `Read`, `Write`, `Edit` | Works | Client-executed. Proxy just relays the `tool_use` / `tool_result` cycle. |
| `NotebookEdit` | Works | Same as above. |
| Custom function tools declared by the client | Works | Translated to OpenAI `function` tools with the same schema. |
| Thinking / reasoning blocks | Works | Mapped to `thinking_delta` and `reasoning_content` / `reasoning_text` upstream events. |
| `WebSearch` (Anthropic server tool `web_search_*`) | **Not supported** | Anthropic executes this server-side. OpenAI backends have no equivalent, so the proxy strips server-tool declarations before forwarding. Disable WebSearch in your Claude Code config or route that model to a real Anthropic backend. |
| `WebFetch` (server tool `web_fetch_*`) | **Not supported** | Same reason as WebSearch. |
| `computer_use` / `code_execution` server tools | **Not supported** | Provider-side only; stripped before upstream call. |
| Image content blocks (`image` in user/assistant messages) | **Not supported** | Adapter returns `invalid_request` for user-side image blocks. |
| MCP tool blocks (`mcp_tool_use`) | **Not supported** | Stripped from replayed history so upstream sees a clean function-call transcript. |

Everything else the model decides to invoke as a regular function-style
tool will pass through correctly with arguments streamed as
`input_json_delta`.

## Limitations

- No image content support in either adapter (returns `invalid_request` on
  image blocks).
- No Anthropic server tools — `web_search`, `web_fetch`,
  `code_execution`, `computer_use`, `mcp_*` are stripped before the
  upstream call since non-Anthropic backends cannot execute them. See
  the **Tool compatibility** table above.
- No WebSocket Responses transport (the path codex uses for
  `previous_response_id`). HTTP-only for now; cache chaining relies on
  upstream prompt caching, which has proven sufficient in practice.
- No persistence across restarts. The session map is in-memory only.
- No retry/backoff. One HTTP attempt per incoming request.

## Development

```bash
go build ./...
go vet ./...

go build -o relaycode ./cmd/relaycode
./relaycode -config relaycode.yaml
```

No external test framework — tests that exist are plain `go test`.

## License

MIT. See `LICENSE` (to be added).
