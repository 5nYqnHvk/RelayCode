# RelayCode

<p align="center">
  <strong>Run Claude Code on GPT-5.5. Or DeepSeek. Or anything OpenAI-compatible.</strong><br/>
  <em>Single-binary proxy. Upstream prompt caching. Up to ~98% input token reuse in one observed long-session workload</em>
</p>

<p align="center">
  <a href="https://github.com/5nYqnHvk/RelayCode/releases"><img alt="Release" src="https://img.shields.io/github/v/release/5nYqnHvk/RelayCode?style=flat-square"></a>
  <a href="https://github.com/5nYqnHvk/RelayCode/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/5nYqnHvk/RelayCode/ci.yml?branch=main&style=flat-square"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/5nYqnHvk/RelayCode?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/go-1.26-00ADD8?style=flat-square">
  <img alt="Platforms" src="https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20windows-555?style=flat-square">
</p>

<p align="center">
  <a href="#why-this-exists">Why this exists</a> ·
  <a href="#install">Install</a> ·
  <a href="#quickstart">Quickstart</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#providers">Providers</a> ·
  <a href="#security">Security</a> ·
  <a href="#faq">FAQ</a> ·
  <a href="#troubleshooting">Troubleshooting</a>
</p>

---

RelayCode sits between **Claude Code** and model backends. It accepts Anthropic
Messages API requests at `/v1/messages`, routes each request by incoming Claude
model name, then streams Anthropic-shaped SSE back to the client.

Supported upstream protocols:

- OpenAI Chat Completions (`openai_chat`, `POST /v1/chat/completions`)
- OpenAI Responses (`openai_responses`, `POST /v1/responses`)
- Native Anthropic Messages passthrough (`anthropic_messages`, `POST /v1/messages`)

Common use: keep Claude Code UX while routing Opus/Sonnet/Haiku requests to
OpenAI-compatible backends, DeepSeek-style chat endpoints, or Anthropic native
routes.

## Why this exists

Claude Code normally sends conversation history every request. Long agentic
sessions get expensive fast: each tool cycle adds more transcript, and later
requests can replay tens of thousands of input tokens.

RelayCode was built to test a different shape: keep Claude Code as the local
agent UI, but route model calls through a Responses-style backend with upstream
prompt caching.

During development, RelayCode itself was built while running Claude Code
through a GPT-5.5 Responses route. Total model spend for that end-to-end build
and compatibility pass was roughly under $150.

Observed token/cost behavior from that run:

| Scenario | Observed result |
|---|---|
| First request in a session | ~20k-30k input tokens |
| Later cached requests | usually ~1k-2k input tokens |
| Claude Code tool-compatibility test | ~40 requests for about $2-$3 |
| Metrics tracked | request count, cost, input/output tokens, cache read/write |

Real RelayCode log from one session (abbreviated, showing the first full replay
and later upstream cache hits via `cached_tokens`; when experimental
`previous_response_id` is enabled, cache reuse appears as `session_chain`):

```text
responses: full_replay provider=custom_provider_responses model=gpt-5.5 reason="codex-compatible http replay" prompt_cache=miss cached_tokens=0 input_tokens=24871 output_tokens=147 stop_reason=end_turn resp=resp_...
responses: full_replay provider=custom_provider_responses model=gpt-5.5 reason="codex-compatible http replay" prompt_cache=hit cached_tokens=24576 input_tokens=24994 output_tokens=43 stop_reason=tool_use resp=resp_...
responses: full_replay provider=custom_provider_responses model=gpt-5.5 reason="codex-compatible http replay" prompt_cache=hit cached_tokens=24576 input_tokens=25059 output_tokens=24 stop_reason=tool_use resp=resp_...
responses: full_replay provider=custom_provider_responses model=gpt-5.5 reason="codex-compatible http replay" prompt_cache=hit cached_tokens=26624 input_tokens=26946 output_tokens=69 stop_reason=end_turn resp=resp_...
responses: session_chain provider=custom_provider_responses model=gpt-5.5 prev=resp_... tail_messages=1 total_messages=12 cached_tokens=0 input_tokens=1119 output_tokens=30 stop_reason=end_turn resp=resp_...
```

Those numbers are workload/provider dependent, but the pattern is the point:
once the stable prefix lands in upstream cache, later Claude Code turns stop
paying full-history cost every request.

<details>
<summary><b>Screenshot — token/cost per request (click to expand)</b></summary>

![Token and cost usage per request, captured during the build run](Img/Token.png)

</details>

## Highlights

- **Single Go binary.** No third-party Go dependencies.
- **Model-aware routing.** Case-insensitive substring match on incoming Claude
  model names, plus required `"*"` fallback.
- **Streaming translation.** Emits Anthropic SSE lifecycle with text, thinking,
  tool use, tool input deltas, stop reasons, and token counts where available.
- **Image blocks.** Claude Code base64 image blocks translate to OpenAI Chat
  `image_url` parts and Responses `input_image` parts.
- **Responses cache keying.** `openai_responses` sets `prompt_cache_key` from
  Claude Code `metadata.user_id.session_id` when present.
- **Claude Code fast paths.** Optional local shortcuts for quota probe, command
  prefix detection, title generation, suggestion mode, and filepath extraction.
- **Local web server tools.** Optional local handling for forced Anthropic
  `web_search` / `web_fetch` requests.
- **Debug stats.** `/debug/stats` exposes in-memory session cache counters.

## Install

### Prebuilt binary (recommended)

Grab the latest release from
[GitHub Releases](https://github.com/5nYqnHvk/RelayCode/releases). Archives
ship the `relaycode` binary, `relaycode.example.yaml`, `README.md`, and
`LICENSE`. Per-archive `sha256` files are published alongside the assets.

Linux / macOS:

```bash
VERSION=1.4.0
curl -L -o relaycode.tar.gz \
  "https://github.com/5nYqnHvk/RelayCode/releases/download/${VERSION}/relaycode-${VERSION}-linux-amd64.tar.gz"
tar -xzf relaycode.tar.gz
./relaycode
# If relaycode.yaml is missing, RelayCode writes one from its embedded example.
# Interactive terminals can choose continue/exit; non-interactive runs exit after writing.
```

Windows: download the matching `*.zip`, unzip, then run `relaycode.exe`.

### Build from source

```bash
go build -o relaycode ./cmd/relaycode
```

## Quickstart

```bash
go build -o relaycode ./cmd/relaycode
./relaycode
# If relaycode.yaml is missing, RelayCode writes one from its embedded example.
# Interactive terminals can choose continue/exit; non-interactive runs exit after writing.
```

Edit `relaycode.yaml`, set provider keys, then run:

```bash
export OPENAI_API_KEY=sk-...
./relaycode -config relaycode.yaml
```

Point Claude Code at RelayCode:

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_AUTH_TOKEN=freecc   # match server.auth_token when configured
claude
```

If `server.auth_token` is empty, RelayCode does not require client auth.

Health check:

```bash
curl http://127.0.0.1:8080/health
```

## How it works

```text
┌──────────────┐   Anthropic        ┌──────────────┐   OpenAI/Anthropic   ┌──────────────┐
│ Claude Code  │ ─── /v1/messages ─▶│  RelayCode   │── chat/responses ───▶│  upstream    │
│   client     │ ◀─── SSE stream ───│              │◀── SSE stream ──────│  provider    │
└──────────────┘                    └──────────────┘                      └──────────────┘
```

Per request, RelayCode:

1. Decodes Anthropic Messages request body.
2. Runs enabled Claude Code fast-path optimizations when request shape matches.
3. Resolves route from `routes[]` using incoming `model`.
4. Handles forced local `web_search` / `web_fetch` when enabled.
5. Builds provider-specific upstream request.
6. Streams upstream SSE back as Anthropic SSE.
7. Updates in-memory Responses session stats when usage data exists.

## Repository layout

```text
cmd/relaycode/                  entrypoint, config flag, signal shutdown
internal/anthropic/             Anthropic request/content types and helpers
internal/config/                stdlib-only YAML subset loader
internal/optim/                 Claude Code fast-path response shortcuts
internal/provider/              adapter interfaces, HTTP/SSE helpers
internal/provider/anthropic/    native Anthropic Messages passthrough adapter
internal/provider/chat/         OpenAI Chat Completions adapter
internal/provider/responses/    OpenAI Responses adapter
internal/router/                model route resolver
internal/server/                HTTP ingress, auth, /health, /debug/stats
internal/session/               in-memory Responses cache/stat store
internal/sse/                   Anthropic SSE writer/builder
internal/streamparse/           thinking/tool-call text parsers
internal/webtools/              local web_search/web_fetch implementation
```

## Configuration

`relaycode.example.yaml`:

```yaml
server:
  host: 127.0.0.1
  port: 8080
  auth_token: ""   # when non-empty, clients must send matching x-api-key / Authorization

  # Local Anthropic web_search/web_fetch handler. Disabled by default because it
  # performs outbound HTTP from the proxy. Runs only when tool_choice forces it.
  enable_web_server_tools: false
  web_fetch_allowed_schemes: http,https
  web_fetch_allow_private_networks: false

  # Claude Code fast-path optimizations. Disable individually for debugging.
  fast_prefix_detection: true
  enable_network_probe_mock: true
  enable_title_generation_skip: true
  enable_suggestion_mode_skip: true
  enable_filepath_extraction_mock: true
  log_request_snapshots: false
  compact_tool_results: false
  enable_update_notification: false
  # update_check_url: https://api.github.com/repos/5nYqnHvk/RelayCode/releases/latest
  # update_check_timeout_seconds: 3
  responses_session_store_path: ""  # optional durable Responses session/cache metadata JSON

routes:
  - match: "opus"
    provider: openai_responses
    model: gpt-5.5
  - match: "sonnet"
    provider: openai_responses
    model: gpt-5.4
  - match: "*"
    provider: deepseek_chat
    model: deepseek-chat

providers:
  openai_responses:
    kind: openai_responses
    base_url: https://api.openai.com/v1
    api_key: "${OPENAI_API_KEY}"
    # http_timeout_seconds: 300
    # http_proxy: "${HTTPS_PROXY}"
    # max_retries: 2
    # max_concurrency: 4
    # codex_auth_path: /home/you/.codex/auth.json
    # experimental_previous_response_id: false
    # experimental_passthrough_server_tools: true
    # responses_custom_tool_mode: native # native|function; function downgrades custom tools for stricter gateways
    # responses_namespace_tools: false # group mcp__server__tool declarations as Responses namespace tools

  openai_chat:
    kind: openai_chat
    base_url: https://api.openai.com/v1
    api_key: "${OPENAI_API_KEY}"

  anthropic_native:
    kind: anthropic_messages
    base_url: https://api.anthropic.com/v1
    api_key: "${ANTHROPIC_API_KEY}"

  deepseek_chat:
    kind: openai_chat
    base_url: https://api.deepseek.com/v1
    api_key: "${DEEPSEEK_API_KEY}"
```

Config rules:

- `${VAR}` values expand from process environment at startup.
- YAML parser supports simple nested maps, lists of maps, and scalar values.
  No anchors, flow style, or multiline strings.
- `routes[].match` is case-insensitive substring match against incoming Claude
  model name. First match wins.
- Fallback route with `match: "*"` is required.
- `providers.<name>.kind` must be `openai_chat`, `openai_responses`, or
  `anthropic_messages`.
- Provider adapters are created lazily on first routed request. Missing API key
  only fails when that provider is used.
- `auth_token`, when non-empty, accepts either `x-api-key: <token>`,
  `Authorization: Bearer <token>`, or raw `Authorization: <token>`.

## Providers

### `openai_responses`

Translates Anthropic messages to OpenAI Responses `input[]` items.

Behavior:

- Sends `model`, `input`, `stream: true`, and `instructions` from Anthropic
  system text.
- Maps `max_tokens` to `max_output_tokens`.
- Forwards `top_p`, tools, and function-call outputs.
- Omits `temperature` because current Responses targets used by RelayCode reject
  or ignore it inconsistently.
- Always sends `tool_choice`, `parallel_tool_calls: false`, and `store: false`.
- Sets `prompt_cache_key` from Claude Code session id when available.
- Adds `include: ["reasoning.encrypted_content"]` when reasoning is requested.
- Maps Anthropic `tool_choice: {"type":"any"}` to OpenAI `required`.
- Drops replayed raw Anthropic thinking blocks because Responses API does not
  accept them.
- Maps Claude Code base64 image blocks to Responses `input_image` parts.

Optional knobs:

- `codex_auth_path`: reads local Codex auth JSON and uses
  `tokens.access_token` as `Authorization: Bearer ...`; also forwards
  `tokens.account_id` as `ChatGPT-Account-ID` when present. Useful when
  targeting OpenAI endpoints that expect Codex-style ChatGPT auth instead of
  normal API key auth.
- `experimental_previous_response_id`: enables HTTP `previous_response_id`
  chaining for backends that support it. Default stays off for Codex-style full
  replay with `prompt_cache_key`.
- `experimental_passthrough_server_tools`: passes Anthropic server tool
  declarations upstream instead of stripping unsupported server-tool entries.
  Keep off unless upstream provider understands those tool shapes.
- `responses_custom_tool_mode: function`: sends schema-less Anthropic custom
  tools as normal Responses function tools with an `input` string argument for
  gateways that reject Responses `custom` tool declarations. Default `native`
  keeps OpenAI/Codex-style custom tools.
- `responses_namespace_tools: true`: groups MCP-style tool names like
  `mcp__calendar__create_event` into Responses `namespace` declarations and
  maps namespace-qualified function calls back to Claude Code's full tool name.
  Default `false` keeps flat function tools for stricter gateways.

### `openai_chat`

Translates Anthropic messages to OpenAI Chat Completions `messages[]`.

Behavior:

- Sends system text as `role: system` message.
- Converts regular client tools to OpenAI function tools.
- Streams chat text, reasoning content, and tool-call arguments back to
  Anthropic SSE.
- Sanitizes tool parameter property named `type` to avoid provider schema bugs,
  then restores argument key on streamed tool input.
- Rejects user image blocks.

### `anthropic_messages`

Passes Anthropic request through to upstream `/v1/messages` with model replaced
by routed upstream model.

Behavior:

- Sends `x-api-key` and `anthropic-version: 2023-06-01`.
- Forces `stream: true`.
- Adds `max_tokens: 4096` when missing or zero.
- Pipes upstream Anthropic SSE through with minor policy transforms.

## Tool compatibility

| Claude Code feature | Status | Notes |
|---|---|---|
| Client tools (`Bash`, `Read`, `Write`, `Edit`, etc.) | Works | RelayCode relays function-style tool calls/results. |
| Custom function tools | Works | Converted to provider function tools. |
| Tool argument streaming | Works | Mapped to Anthropic `input_json_delta`. |
| Thinking/reasoning deltas | Works | Chat reasoning and Responses reasoning events map to `thinking_delta`. |
| Local `web_search` / `web_fetch` | Optional | Requires `server.enable_web_server_tools: true` and forced Anthropic server tool choice. |
| Provider-side server tools | Experimental | Use `experimental_passthrough_server_tools` only with compatible upstreams. |
| Images | Works | Claude Code base64 image blocks map to Chat `image_url` and Responses `input_image`. |
| MCP/server-tool replay blocks | Degraded by default | Preserves model-visible history as text unless passthrough is enabled. |
| Responses namespace tools | Optional | `responses_namespace_tools: true` groups `mcp__server__tool` names into Codex-style namespace declarations. |
| Chained custom tool results | Works | Stored `call_id` metadata lets `previous_response_id` tails emit `custom_tool_call_output`. |

Claude Code tool probe on 2026-05-14 verified safe, reversible client tools through RelayCode: agent dispatch, shell foreground/background tasks, file read/write/edit, notebook edit, task list/update/output/stop, cron create/list/delete, monitor events, web fetch/search, and plan-mode entry/exit. Worktree and dynamic loop wakeups were not exercised because they require explicit workflow context. `PushNotification` is a known caveat from that probe: the adapter dropped the tool call due schema validation.

## Observability

Stats endpoint:

```bash
curl -sS http://127.0.0.1:8080/debug/stats \
  -H "x-api-key: freecc" | jq .
```

Response shape:

```json
{
  "counters": {
    "hits": 0,
    "misses": 0,
    "forced_replays": 0,
    "expired_invalid": 0,
    "input_tokens": 0,
    "output_tokens": 0
  },
  "sessions": [
    {
      "provider": "openai_responses",
      "upstream_model": "gpt-5.5",
      "message_count": 3,
      "response_id": "resp_...",
      "last_used": "2026-05-11T05:32:50Z",
      "input_tokens": 1119,
      "output_tokens": 30
    }
  ]
}
```

Debug logging:

- `RELAYCODE_DEBUG_REQUEST=1`: logs raw incoming `/v1/messages` JSON. Use only
  locally; this can include prompt text.
- `server.log_request_snapshots: true` or `RELAYCODE_LOG_REQUEST_SNAPSHOTS=1`:
  logs scrubbed request shape snapshots without raw prompt text.
- `server.compact_tool_results: true`: sends compacted long tool/Bash outputs to
  OpenAI-compatible upstreams while keeping short outputs unchanged. Raw capture
  files remain full when `RELAYCODE_CAPTURE_DIR` is enabled.
- `server.enable_update_notification: true`: checks the latest GitHub Release tag
  once at startup and logs when a newer release exists. Source builds use version
  `dev` and skip update checks; release builds get their tag injected at build time.
- `server.update_check_url` and `server.update_check_timeout_seconds`: override
  the release endpoint and timeout (defaults: GitHub latest release API, 3s).
- `RELAYCODE_CAPTURE_DIR=/tmp/relaycode-capture`: writes one directory per
  request with `incoming_anthropic.json`, per-call `upstream/*/request.json`, and
  split SSE frames under `upstream/*/events/` and `downstream_events/`. Use only
  with throwaway prompts; request and tool content are captured for fixture generation.

## Limitations

- Session store is in memory by default. Set `server.responses_session_store_path`
  to persist Responses session/cache metadata JSON and tool-call metadata across
  restarts.
- Responses cache reuse relies on upstream prompt caching via `prompt_cache_key`.
  Optional HTTP `previous_response_id` chaining is experimental; WebSocket
  continuation is not implemented.
- OpenAI image support expects Claude Code base64 image blocks; remote image URLs are not fetched by RelayCode.
- Local web tools run only for forced Anthropic web server tool requests.
- Retry applies to transport errors, HTTP 429/5xx before a stream is accepted,
  and early Responses stream failures before any content is emitted. Later
  mid-stream provider failures are returned as Anthropic SSE errors.

## Security

- **Bind to localhost by default.** `server.host: 127.0.0.1` in
  `relaycode.example.yaml`. Bind to a public interface only when needed.
- **Set `server.auth_token`** before exposing RelayCode beyond localhost.
  Without it, any local process can reach the proxy.
- **No TLS termination.** RelayCode serves plain HTTP. Terminate TLS at a
  reverse proxy (Caddy, nginx, Cloudflare Tunnel) when not on localhost.
- **No prompt logging by default.** `RELAYCODE_DEBUG_REQUEST=1` prints raw
  request bodies; use only locally for debugging. `log_request_snapshots`
  prints shape-only snapshots without raw prompt text. `compact_tool_results`
  can reduce long Bash/tool replay sent upstream. `RELAYCODE_CAPTURE_DIR` writes
  raw request/tool content for local fixture capture only.
- **No outbound update checks by default.** `enable_update_notification` must be
  set explicitly before RelayCode calls the GitHub Release API.
- **Provider keys via env.** Prefer `${OPENAI_API_KEY}` / `${DEEPSEEK_API_KEY}`
  over pasting keys into `relaycode.yaml`.
- **Session store is in memory by default.** Set
  `server.responses_session_store_path` if you want Responses session/cache and
  tool-call metadata on disk.

## FAQ

**Why route through Responses instead of Chat Completions?**
Responses accepts `prompt_cache_key`, so multi-turn Claude Code sessions reuse
the shared prefix upstream. Chat Completions works but has no session-level
cache handle.

**Can I keep using Anthropic directly?**
Yes. Configure an `anthropic_messages` provider and route whichever model
substring you want through it. RelayCode just forwards the request.

**How do I mix providers?**
Put multiple entries in `routes[]`. First match (case-insensitive substring on
incoming model) wins. Example: `opus` → OpenAI Responses, `haiku` → DeepSeek
chat, `*` → fallback.

**Does it support image / vision?**
Yes. Claude Code base64 image blocks map to OpenAI Chat `image_url` parts and
Responses `input_image` parts. Native Anthropic routes pass image blocks through.

**Will Claude Code know it's being proxied?**
No. RelayCode speaks the Anthropic Messages API; Claude Code treats it as a
normal Anthropic endpoint.

## Troubleshooting

- **`prompt_cache=miss` every turn.** Client may not be sending
  `metadata.user_id.session_id`, or upstream may not be honoring
  `prompt_cache_key`. Without a session id, RelayCode falls back to
  instructions/tools fingerprint when available.
- **`401`/`403` from upstream.** API key is missing or wrong. Check the env
  var referenced in `relaycode.yaml`.
- **`429` from upstream.** Set `providers.<name>.max_retries` and
  `max_concurrency` to smooth spikes.
- **Long requests time out.** Bump `providers.<name>.http_timeout_seconds`.
- **Image blocks.** OpenAI adapters now accept Claude Code base64 image blocks.
  Use native Anthropic only if you want direct passthrough.
- **Forced `web_search` / `web_fetch` returns 400.** Enable
  `server.enable_web_server_tools: true`. Default is off because the proxy
  makes outbound HTTP.

## Development

```bash
go test ./...
go vet ./...
go build -o relaycode ./cmd/relaycode
./relaycode -config relaycode.yaml
```

No external Go dependencies. Tests use standard `go test`.

## License

MIT. See `LICENSE`.
