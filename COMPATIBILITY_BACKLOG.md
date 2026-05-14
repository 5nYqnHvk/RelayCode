# Compatibility Backlog

This file tracks Claude Code / Codex parity work after the Responses/custom-tool compatibility pass. RelayCode's Go scope remains easy installation, OpenAI Chat fallback, and OpenAI Responses/cache-focused translation rather than full `../cc` feature breadth.

## Completed In Current Pass

### Responses Custom/Freeform Tool Surface

Schema-less Anthropic `custom` tools now serialize as Responses `custom` tool declarations with text format. Responses custom tool call deltas map back to Anthropic `tool_use`, and full replay can emit `custom_tool_call` / `custom_tool_call_output` items when the tool name is known from the transcript.

### Server And MCP Tool Replay Preservation

OpenAI routes no longer silently erase unsupported server/MCP history by default. When passthrough is disabled, server/MCP blocks are degraded to text summaries so resumed transcripts retain model-visible context without pretending the upstream can execute Anthropic server tools.

### Structured Output And Beta Body Policy

OpenAI Chat and Responses now use explicit structured-output handling: `json_schema`, `json_object`, and `text` are accepted; malformed or unsupported formats return clear errors. Anthropic-only body fields such as `context_management` and unknown extras are rejected on OpenAI routes instead of being silently ignored.

### Minimal Model Listing

`GET /v1/models` returns a static OpenAI-style list derived from configured routes. It does not probe upstream providers.

### Durable Session Store

`server.responses_session_store_path` enables optional JSON persistence for Responses session metadata and stats. Loaded entries are TTL-pruned and still fall back to full replay when upstream rejects a persisted `previous_response_id`.

### Image Translation

Claude Code base64 image blocks now map to OpenAI Chat `image_url` parts and Responses `input_image` parts. Native Anthropic routes still passthrough the original block shape.

### Provider-Specific Responses Custom Tool Mode

`providers.<name>.responses_custom_tool_mode: function` can downgrade schema-less Anthropic `custom` tools to normal Responses function tools for OpenAI-compatible gateways that reject Responses `custom` declarations. Default `native` keeps OpenAI/Codex-style custom/freeform tools.

### Codex Namespace Tool Declarations

`providers.<name>.responses_namespace_tools: true` can group `mcp__server__tool` function declarations into Responses `namespace` tools, replay namespace-qualified calls, and map streamed namespaced function calls back to Claude Code's full Anthropic tool names. Default `false` keeps flat function declarations for OpenAI-compatible gateways without namespace support.

### Stronger Custom Tool Output In Chained Tails

Responses session entries now persist `call_id -> function|custom` metadata from streamed assistant tool calls. Tail-only `previous_response_id` tool results can therefore emit `custom_tool_call_output` when the matching custom call exists only in upstream state.

### Provider-Specific Responses Drift

Fixture coverage now includes namespace tool declarations/streaming and custom-tool previous-response tails, alongside native/function custom-tool modes. Keep adding config-gated drift behavior only when real target gateways require it.

## Remaining Work

No known Claude Code / Codex compatibility backlog items remain in current Go scope.
