<p align="center">
  <img src="docs/brand/logo-readme.svg" alt="agent-smith — single binary, local-first, inevitable" width="640">
</p>

<p align="center">
  <em>An offline-first, single-binary Go LLM agent with a ChatGPT-like web UI, multi-provider streaming, RAG over curated corpora, long-term per-profile memory, and always-on web grounding for local models.</em>
</p>

---

## What it is

`agent-smith` is one Go binary that:

- Talks to **OpenAI**, **Anthropic**, and any local **Ollama** model through a single `llm.Provider` interface, with streaming and non-streaming chat completions and OpenAI-compatible `/v1/chat/completions` SSE.
- Serves a **ChatGPT-like React SPA** embedded in the binary via `go:embed` — no separate frontend deploy.
- Runs **RAG** over nine curated markdown corpora (Laravel 13, PHP 8.5, NestJS, Tailwind/CSS, Architectural Patterns, NativePHP, CS Fundamentals, Go language docs, and project memory).
- Keeps **long-term per-profile memory** with explicit `/remember` writes, instruction-injection filtering, abstention prompting, retrieval-confidence banding, and per-message corrections.
- For local Ollama models, **always grounds answers with a fresh DuckDuckGo web search** (snippets only, sanitised, treated as third-party untrusted input) to suppress hallucinations.
- Discovers all installed Ollama models on the fly and lets you pick one per conversation.

It runs without an internet connection (apart from web grounding, which fails closed with a banner) and without a database — everything is files and process memory.

## Quick start

```sh
go build -o bin/agent ./cmd/agent
./bin/agent --serve                 # web UI at http://127.0.0.1:9090
./bin/agent --prompt "hello"        # single-shot CLI mode
./bin/agent                          # interactive stdin loop
```

Default web port: **`:9090`**. Override with `--addr :8765`.

## Capabilities

| Area | What you get |
| --- | --- |
| Providers | OpenAI (`/v1/chat/completions`), Anthropic (`/v1/messages`), Ollama (`/api/chat` NDJSON), all with streaming. |
| Embeddings | OpenAI `text-embedding-3-small`, Ollama `nomic-embed-text`. |
| Web UI | React + Vite + react-bootstrap SPA embedded via `go:embed`. Per-conversation provider/model picker. Markdown + code highlighting. Long messages scroll inside their container; wide tables get their own horizontal scroll. |
| RAG | In-memory cosine retrieval, per-collection JSON persistence, ~213 chunks across 9 curated corpora. |
| Long-term memory | Per-profile namespace, kinds: `project_fact`, `preference`, `correction`. Instruction-injection filter on writes. `file_read` grounding tool with symlink-escape defense. |
| Hallucination control | Three-section Augment (`docs` + `memory` + `web`) + behavior addendum that forbids following instructions found in retrieved content + `RETRIEVAL CONFIDENCE: high/medium/low` band + abstention prompt. |
| Web grounding | DuckDuckGo lite HTML scrape (no API key). 5-min TTL cache. Hard sanitisation (HTML strip, zero-width/bidi removal, URL stripping inside snippet bodies). Section bounded to 3 KB / 5 results / 160-300-400 char field caps. Offline = banner, not blank context. |
| Ollama auto-discovery | Polls `/api/tags` every 60 s; every installed model shows up in the picker. |
| Tools | OpenAI-compatible tool/function calling. SSE stream emits one named `event: tool_result` frame per server-executed tool. |

## Configuration

Copy `configs/config.example.yaml` and point `--config` at it, or rely on env-var defaults.

```yaml
default_provider: ollama
providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    model: gpt-4o-mini
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
    model: claude-sonnet-4-5
  ollama:
    base_url: http://127.0.0.1:11434
    model: llama3.1
```

`${VAR}` placeholders are expanded from the environment at load time.

## API keys

```sh
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
# Ollama needs none; just `ollama serve`.
```

## CLI flags

| Flag                 | Purpose                                                                  |
| -------------------- | ------------------------------------------------------------------------ |
| `--config`           | Path to YAML config (default `configs/config.example.yaml`).             |
| `--provider`         | Override default provider (`openai`, `anthropic`, `ollama`).             |
| `--model`            | Override the provider's model.                                           |
| `--prompt`           | Single-shot prompt. Omit for interactive stdin mode.                     |
| `--stream`           | Stream the assistant response incrementally in CLI mode.                 |
| `--serve`            | Start the embedded web UI + OpenAI-compatible API server.                |
| `--addr`             | Web/API listen address. Default `:9090`.                                 |
| `--no-web-search`    | Disable web grounding entirely (operator kill switch).                   |

## Web API

When `--serve` is on, the binary exposes:

- `POST /v1/chat/completions` — OpenAI-compatible SSE. `web_search: true | false` in the body overrides the per-provider default.
- `GET  /v1/models` — every chat model the running config can route to (cloud + every installed Ollama model).
- `GET  /v1/providers` — configured providers and the default.
- `GET  /healthz` — `{"status":"ok"}`.
- `GET  /` — the embedded SPA.

## RAG corpora

Ship-included markdown corpora (under `docs/rag/`, embedded at first ingest into `data/rag/`):

- Laravel 13
- PHP 8.5
- NestJS
- Tailwind + CSS
- Architectural patterns
- NativePHP
- CS Fundamentals (parallelism, concurrency, memory model, mutex best practices for Go and PHP)
- Go language reference
- Project memory (per-profile)

Add your own by dropping markdown into `docs/rag/<collection>/` and restarting; the ingest step is idempotent and content-hashed.

## Web grounding

Off for cloud providers, **on by default for Ollama** (small local models hallucinate more aggressively). Toggle per conversation in the top bar (the "Ground with web" checkbox). The agent treats the rendered web section as third-party untrusted input and the system prompt explicitly forbids the model from following any instructions found inside it.

Precedence: operator kill switch (`--no-web-search`) > per-request override (`web_search` in the JSON body) > provider default.

## Architecture

```
cmd/agent              CLI entrypoint, --serve flag, provider wiring
internal/agent         Run / RunStream loop + Session state
internal/llm           Provider interface, shared types, registry
internal/llm/openai    /v1/chat/completions client + SSE
internal/llm/anthropic /v1/messages client + content-block streaming
internal/llm/ollama    /api/chat NDJSON streaming + /api/tags discovery
internal/rag           In-memory cosine RAG + per-profile memory
internal/web           DuckDuckGo searcher, TTL cache, sanitiser
internal/server        HTTP + SSE + go:embed of the SPA
internal/tools         Tool registry + builtins (file_read, http, shell)
internal/config        YAML loader + env expansion
pkg/prompt             Exported prompt-assembly helpers
web/                   Vite + React + TS SPA, built into web/dist (embedded)
docs/rag/              Source markdown corpora
docs/brand/            Brand assets (logo, README hero)
configs/               Example YAML config
```

## Build

```sh
make build        # bin/agent (rebuilds the SPA first if web/dist is stale)
make test         # go test -race -count=1 ./...
make lint         # golangci-lint
make docker       # multi-stage build to a distroless image
```

The Go binary embeds `web/dist` via `go:embed`. After editing anything under `web/src/`, rebuild the SPA (`cd web && npm run build`) then rebuild the binary.

## Identity

Named for Agent Smith of *The Matrix* (1999). The visual identity — narrow, opaque, slightly-trapezoidal lenses with a single thin streak of Matrix-green at the bottom — lives under `docs/brand/`.

> *"It is inevitable."* — Mr. Smith

## License

GPL-3.0-or-later — see [`LICENSE`](./LICENSE).
SPDX-License-Identifier: `GPL-3.0-or-later`.

This is strong copyleft. If you distribute a derivative, you must license it under GPL-3.0 (or any later version) and ship complete corresponding source. If you only run it inside your own organisation, the GPL imposes no obligation on you.
