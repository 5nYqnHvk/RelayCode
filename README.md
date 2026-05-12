# RelayCode

<p align="center">
  <strong>Run Claude Code through OpenAI-compatible, DeepSeek-style, or Anthropic backends.</strong><br/>
  <em>Single Go binary. Anthropic Messages in, Anthropic SSE out. Responses routes can reuse upstream prompt cache.</em>
</p>

<p align="center">
  <a href="https://github.com/5nYqnHvk/RelayCode/releases"><img alt="Release" src="https://img.shields.io/github/v/release/5nYqnHvk/RelayCode?style=flat-square"></a>
  <a href="https://github.com/5nYqnHvk/RelayCode/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/5nYqnHvk/RelayCode/ci.yml?branch=main&style=flat-square"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/5nYqnHvk/RelayCode?style=flat-square"></a>
  <img alt="Go" src="https://img.shields.io/badge/go-1.26-00ADD8?style=flat-square">
  <img alt="Platforms" src="https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20windows-555?style=flat-square">
</p>

RelayCode is a local proxy for Claude Code. It accepts Anthropic Messages API requests at `/v1/messages`, routes by incoming Claude model name, calls a configured upstream provider, then streams Anthropic-shaped SSE back to Claude Code.

Supported upstream protocols:

- OpenAI Chat Completions (`openai_chat`, `POST /v1/chat/completions`)
- OpenAI Responses (`openai_responses`, `POST /v1/responses`)
- Native Anthropic Messages passthrough (`anthropic_messages`, `POST /v1/messages`)

## Why use it

Claude Code sends conversation history every request. Long agent sessions can replay tens of thousands of input tokens per turn. `openai_responses` routes set `prompt_cache_key` from Claude Code session metadata when available, so compatible upstreams can reuse the stable prompt prefix.

Observed long-session build run through a Responses route:

| Scenario | Observed result |
|---|---|
| First request | ~20k-30k input tokens |
| Later cached requests | usually ~1k-2k new input tokens |
| Tool-compatibility test | ~40 requests for about $2-$3 |

These numbers are workload/provider dependent. RelayCode records request count, token usage, and cache hit/miss stats when upstream usage data is available.

<details>
<summary><b>Screenshot — token/cost per request</b></summary>

![Token and cost usage per request, captured during the build run](Img/Token.png)

</details>

## Features

- Single zero-dependency Go binary.
- Case-insensitive model routing with required `"*"` fallback.
- Streaming translation for text, reasoning/thinking, tool calls, tool input deltas, stop reasons, and token counts where available.
- Claude Code fast paths for quota probe, command prefix detection, title generation, suggestion mode, and filepath extraction.
- Optional local handling for forced Anthropic `web_search` / `web_fetch` requests.
- `/debug/stats` for in-memory Responses session/cache counters.

## Install

Download latest archive from [GitHub Releases](https://github.com/5nYqnHvk/RelayCode/releases). Assets include `relaycode`, `relaycode.example.yaml`, `README.md`, `LICENSE`, and matching `.sha256` files.

Example Linux amd64 download:

```bash
VERSION=v0.1.0
curl -L -o relaycode.tar.gz \
  "https://github.com/5nYqnHvk/RelayCode/releases/download/${VERSION}/relaycode-${VERSION}-linux-amd64.tar.gz"
tar -xzf relaycode.tar.gz
cp relaycode.example.yaml relaycode.yaml
./relaycode -config relaycode.yaml
```

Linux/macOS use `.tar.gz`; Windows uses `.zip`.

Or build from source:

```bash
go build -o relaycode ./cmd/relaycode
```

## Quickstart

```bash
go build -o relaycode ./cmd/relaycode
cp relaycode.example.yaml relaycode.yaml
```

Edit `relaycode.yaml`, set provider keys/models, then run:

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

If your Claude Code version expects `ANTHROPIC_API_KEY`, set it to the same token. If `server.auth_token` is empty, RelayCode accepts unauthenticated local requests.

Health check:

```bash
curl http://127.0.0.1:8080/health
```

## Configuration

Minimal shape:

```yaml
server:
  host: 127.0.0.1
  port: 8080
  auth_token: ""
  enable_web_server_tools: false
  web_fetch_allowed_schemes: http,https
  web_fetch_allow_private_networks: false
  fast_prefix_detection: true
  enable_network_probe_mock: true
  enable_title_generation_skip: true
  enable_suggestion_mode_skip: true
  enable_filepath_extraction_mock: true
  log_request_snapshots: false

routes:
  - match: "opus"
    provider: openai_responses
    model: gpt-5.5     # example only; use a model your upstream provides
  - match: "sonnet"
    provider: openai_responses
    model: gpt-5.4     # example only; use a model your upstream provides
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
    # experimental_passthrough_server_tools: true

  anthropic_native:
    kind: anthropic_messages
    base_url: https://api.anthropic.com/v1
    api_key: "${ANTHROPIC_API_KEY}"

  deepseek_chat:
    kind: openai_chat
    base_url: https://api.deepseek.com/v1
    api_key: "${DEEPSEEK_API_KEY}"
```

Rules:

- `${VAR}` expands from environment at startup.
- YAML parser supports nested maps, lists of maps, and scalar string/number/bool values. No anchors, flow style, or multiline strings.
- `routes[].match` is case-insensitive substring match against incoming Claude model name. First match wins. `match: "*"` fallback is required.
- `providers.<name>.kind` must be `openai_chat`, `openai_responses`, or `anthropic_messages`.
- Provider adapters are created lazily. Missing API key fails only when that provider is used.
- `auth_token` accepts `x-api-key: <token>`, `Authorization: Bearer <token>`, or raw `Authorization: <token>`.

See `relaycode.example.yaml` for full commented config.

## Provider behavior

### `openai_responses`

- Sends `model`, `input`, `stream: true`, `instructions`, `max_output_tokens`, `top_p`, tools, function-call outputs, `tool_choice`, `parallel_tool_calls: false`, and `store: false`.
- Omits `temperature` because current Responses targets used by RelayCode reject or ignore it inconsistently.
- Sets `prompt_cache_key` from Claude Code session id when available.
- Adds `include: ["reasoning.encrypted_content"]` when reasoning is requested, matching Codex Responses requests.
- Adds a short tool-use bridge instruction for OpenAI models so tool-work requests do not end after a text preamble.
- Maps Anthropic `tool_choice: {"type":"any"}` to OpenAI `required`.
- Drops replayed raw Anthropic thinking blocks and strips unsupported server-tool declarations unless `experimental_passthrough_server_tools` is enabled.
- HTTP Responses follows Codex: full input every turn with `prompt_cache_key`; `previous_response_id` is left off unless `experimental_previous_response_id` is explicitly enabled for a backend that supports HTTP continuation.
- Rejects user image blocks.
- `codex_auth_path` can read local Codex auth JSON and use `tokens.access_token` / `tokens.account_id` for compatible OpenAI endpoints.

### `openai_chat`

- Sends system text as `role: system`.
- Converts client tools to OpenAI function tools.
- Streams text, reasoning content, and tool-call arguments back to Anthropic SSE.
- Sanitizes tool parameter property named `type`, then restores argument key on streamed tool input.
- Rejects user image blocks.
- Cannot use listed Anthropic `web_search` / `web_fetch` server tools unless local web server tools are enabled.

### `anthropic_messages`

- Passes request through to upstream `/v1/messages` with routed model replacement.
- Sends `x-api-key` and `anthropic-version: 2023-06-01`.
- Forces `stream: true` and adds `max_tokens: 4096` when missing or zero.
- Pipes upstream Anthropic SSE through with minor policy transforms.

## Tool compatibility

| Claude Code feature | Status | Notes |
|---|---|---|
| Client tools (`Bash`, `Read`, `Write`, `Edit`, etc.) | Works | Relayed as function-style tool calls/results. |
| Custom function tools | Works | Converted to provider function tools. |
| Tool argument streaming | Works | Mapped to Anthropic `input_json_delta`. |
| Thinking/reasoning deltas | Works | Chat reasoning and Responses reasoning events map to `thinking_delta`. |
| Local `web_search` / `web_fetch` | Optional | Requires `server.enable_web_server_tools: true` and forced Anthropic server-tool choice. |
| Provider-side server tools | Experimental | `openai_responses` only; enable `experimental_passthrough_server_tools` when upstream supports those tool shapes. |
| Images | Native Anthropic only | OpenAI adapters reject user image blocks. |

Local web tools are lightweight substitutes, not Anthropic-equivalent browsing: `web_search` uses DuckDuckGo Lite and returns at most 5 results; `web_fetch` follows at most 5 redirects, reads at most 1 MiB, and returns at most 20,000 chars.

## Observability

```bash
curl -sS http://127.0.0.1:8080/debug/stats \
  -H "x-api-key: freecc" | jq .
```

Example response:

```json
{
  "counters": {
    "hits": 0,
    "misses": 0,
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

The endpoint may also include reserved counters such as `forced_replays` and `expired_invalid`; current code exposes them but does not increment them yet.

Debug logging:

- `RELAYCODE_DEBUG_REQUEST=1`: logs raw incoming `/v1/messages` JSON. Use only locally; this can include prompt text.
- `server.log_request_snapshots: true` or `RELAYCODE_LOG_REQUEST_SNAPSHOTS=1`: logs scrubbed request-shape snapshots without raw prompt text.

## Security and limits

- Default bind is localhost: `server.host: 127.0.0.1`.
- Set `server.auth_token` before exposing RelayCode beyond localhost.
- `/v1/messages` and `/debug/stats` require auth when `auth_token` is set. `/health` is always unauthenticated on the bound interface.
- RelayCode serves plain HTTP. Use a reverse proxy for TLS outside localhost.
- No prompt logging by default. `RELAYCODE_DEBUG_REQUEST=1` prints raw prompts.
- Session/cache metadata is in memory only and resets on restart.
- Responses cache reuse depends on upstream `prompt_cache_key`; RelayCode does not use WebSocket `previous_response_id` transport.
- Retries apply only to transport errors, HTTP 429, and HTTP 5xx before stream acceptance. Mid-stream provider failures return Anthropic SSE errors.

## Troubleshooting

- **`prompt_cache=miss` every turn:** Claude Code may not be sending `metadata.user_id.session_id`; prefix reuse falls back to instructions/tools fingerprint. `full_replay` is expected on HTTP Responses and only means the full input was sent.
- **`401`/`403` from upstream:** API key missing or wrong. Check env var referenced in `relaycode.yaml`.
- **`429` from upstream:** set `providers.<name>.max_retries` and `max_concurrency`.
- **Long requests time out:** raise `providers.<name>.http_timeout_seconds`.
- **`image user blocks not supported`:** route vision turns through `anthropic_messages`.
- **Forced `web_search` / `web_fetch` returns 400:** enable `server.enable_web_server_tools: true`.

## Development

```bash
gofmt -w .
go vet ./...
go test ./...
go build -o relaycode ./cmd/relaycode
```

No external Go dependencies. Tests use standard `go test`.

## License

MIT. See `LICENSE`.
