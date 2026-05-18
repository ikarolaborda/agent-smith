# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] — 2026-05-18

First public release.

### Added
- Single-binary Go LLM agent with pluggable `llm.Provider` interface (OpenAI, Anthropic, local Ollama) and OpenAI-compatible streaming + non-streaming chat completions.
- ChatGPT-like React + Vite + react-bootstrap SPA embedded into the binary via `go:embed`. Per-conversation provider/model picker. Markdown + code highlighting.
- OpenAI-compatible HTTP API: `POST /v1/chat/completions` (SSE), `GET /v1/models`, `GET /v1/providers`, `GET /healthz`.
- Server-emitted tool-call streaming via a single named `event: tool_result` SSE frame; per-`tool_call_id` buffering with anon-N fallback.
- RAG over nine curated markdown corpora (Laravel 13, PHP 8.5, NestJS, Tailwind/CSS, Architectural Patterns, NativePHP, CS Fundamentals, Go language docs, project memory). In-memory cosine retrieval, per-collection JSON persistence, ~213 chunks.
- Long-term per-profile memory with kinds `project_fact` / `preference` / `correction`, instruction-injection filter on writes, `file_read` grounding tool with symlink-escape defense, abstention prompt, `RETRIEVAL CONFIDENCE: high/medium/low` band.
- Always-on web grounding for Ollama (off by default for cloud providers, UI toggle per conversation). DuckDuckGo lite HTML scrape, 5-min TTL cache, hard sanitisation (HTML strip, zero-width/bidi removal, URL stripping inside snippets), per-field caps (160/300/400) + section cap (3 KB).
- Ollama auto-discovery via `/api/tags` every 60 s; every installed model appears in the picker.
- Default port `:9090`. CLI flags: `--serve`, `--addr`, `--no-web-search`, `--config`, `--provider`, `--model`, `--prompt`, `--stream`.
- Visual identity: dark-mode-aware SVG favicon, compact brand mark, and 720×220 README hero based on Agent Smith's narrow sunglasses (Matrix, 1999). All hand-coded SVG.
- Tested under `-race`: 67/67 across 15 packages, including mid-stream cancellation, repeated POSTs, and the web-search precedence rule (kill switch > request override > provider default).

### License
- Released under GPL-3.0-or-later.

[Unreleased]: https://github.com/ikarolaborda/agent-smith/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ikarolaborda/agent-smith/releases/tag/v0.1.0
