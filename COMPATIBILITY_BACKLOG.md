# Compatibility Backlog

This file tracks the remaining Claude Code / Codex parity work after the current tool-call compatibility pass. It is intentionally based on the current implementation notes only; no fresh upstream reference scan was performed for this backlog.

## Remaining Work

### Codex Namespace And Freeform Tool Declarations

RelayCode still serializes model-visible tools as flat function declarations for OpenAI Chat and Responses. Future work should add first-class support for Codex-style namespace tool specs and freeform/custom tool declarations, including provider-specific routing for namespaced MCP tools and non-JSON freeform inputs.

### Server And MCP Tool Replay Semantics

OpenAI routes still drop or simplify many server/MCP history blocks that they cannot execute directly. Future work should preserve enough server/MCP use/result history to replay resumed Claude Code transcripts without losing context, while still guarding unsupported upstream providers.

### Responses Custom Tool Output Parity

Responses streaming now accepts custom tool input deltas, but custom/freeform tool declarations and custom tool output replay are still represented through the regular function-tool path. Future work should add the full Responses custom tool call/output item surface.

### Structured Output Edge Cases

Basic `output_config.format` mapping exists for OpenAI Chat and Responses. Future work should harden schema naming, strict defaults, non-JSON-schema formats, and provider/model compatibility behavior so structured output behaves consistently across routes.

### Beta Body Mapping For Non-Anthropic Routes

Native Anthropic egress preserves `betas`, `context_management`, and unknown body fields. OpenAI routes intentionally ignore most Anthropic-only beta body fields today. Future work should decide which fields can be safely translated, forwarded to compatible gateways, or rejected with clear errors.

### Durable Session Store

Responses chaining is currently in-memory and falls back to full replay when upstream invalidates a `previous_response_id`. Future work could persist session metadata across RelayCode restarts and add stronger output-item baseline validation for incremental continuation.
