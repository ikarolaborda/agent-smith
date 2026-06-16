# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] — 2026-06-16

Clustered inference, an agentic tool loop, live Context7 docs, and an always-on
persona/engineering policy — plus a README that finally matches reality and a
green CI.

### Added
- **Clustered inference** across two Apple-Silicon nodes via `--cluster-config`: `exo`, `mlx_jaccl`, and `llama_cpp_rpc` backends with an automatic `local` (Ollama) single-node fallback, auto/pinned backend selection by health + `preferred_backends`, and `tensor_split` weight/KV distribution.
- **Single-node-first scheduler guard**: a model that fits on one node runs on one node; distribution is used only when a model genuinely doesn't fit. Per-model opt-out via `force_distribute`.
- **Per-node memory budget guards** to avoid the Apple-Silicon memory-pressure kernel panic: `coordinator_reserve_gb` (default 20), `safe_model_gb` (default RAM/2), and a context-aware compute reserve. `strict_cluster` disables the local fallback.
- **Long-context plumbing**: a model's `context_tokens` flows through to the backend as the effective window (`num_ctx` / `--ctx-size`) so large-context security research keeps a usable window. `parallel` maps to llama.cpp `--parallel` (default 1).
- **Agentic tool loop**: `read_dir` loads an entire folder into context like an IDE `@folder` (recursive UTF-8 read, path headers, byte budget, noise-dir/binary skipping, `ext` filter); `shell` and `http` round out the always-on read-only set. Sandboxed `file_write` / `file_edit` are enabled by pointing `--workspace` at a directory.
- **Context7 augmentation**: live, authoritative library docs fetched per request for tech/library questions, bounded timeout, silent failure, rendered as a labelled section. On when `CONTEXT7_API_KEY` is set; `--no-context7` kills it.
- **Always-on persona + engineering policy** injected on every request, provider- and language-agnostic: an informal cybersecurity-software-architect voice, an OOP/Clean-Architecture coding standard, mandatory Context7 for third-party APIs, and a hard factual-grounding rule (never fabricate a CVE, version range, or payload). Guarded by tests.
- New endpoints: `GET /v1/cluster` (live node/backend status), `POST /v1/title`, and the `/v1/rag/*` family (`collections`, `search`, `remember`, `forget`, `memory`, `correction`).
- Cluster-mode indicator in the web UI (`ClusterBadge`, polled from `/v1/cluster`).
- New CLI flags: `--cluster-config`, `--workspace`, `--no-context7`, `--rag-max-chunks`, `--no-rag`, and the ingest set (`--ingest`, `--collection`, `--source`, `--embedder`, `--embed-model`, `--rag-dir`).
- Cybersecurity RAG corpus added to the curated set.
- Example/local cluster configs under `configs/` and a two-Mac bring-up runbook under `docs/`.

### Fixed
- CI lint job is green again: aligned the Go version floor to `1.24` (with a matching `x/net` downgrade), fixed an unchecked SSE write error (`errcheck`), and scoped revive's `exported` rule to keep doc-comment enforcement while dropping the stutter sub-check that collides with the existing `cluster.Manager` type. `gofmt` applied across previously-unformatted files.

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

[Unreleased]: https://github.com/ikarolaborda/agent-smith/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/ikarolaborda/agent-smith/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/ikarolaborda/agent-smith/releases/tag/v0.1.0
