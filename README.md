<p align="center">
  <img src="docs/brand/logo-readme.svg" alt="agent-smith — single binary, local-first, inevitable" width="640">
</p>

<p align="center">
  <em>An offline-first, single-binary Go LLM agent with a ChatGPT-like web UI, multi-provider streaming, agentic tools, RAG over curated corpora, live Context7 docs, long-term per-profile memory, web grounding — and a two-node Apple-Silicon inference cluster for running 70B-class models that don't fit on one machine.</em>
</p>

---

## What it is

`agent-smith` is one Go binary that:

- Talks to **OpenAI**, **Anthropic**, any local **Ollama** model, and a self-managed **llama.cpp** server through one `llm.Provider` interface — streaming and non-streaming, with an OpenAI-compatible `/v1/chat/completions` SSE endpoint.
- Resolves immutable Hugging Face GGUF manifests, profiles live host memory/disk, refuses unsafe fits before payload transfer, verifies shards/projectors by SHA-256, and supervises `llama-server` on loopback. See [`docs/llamacpp-local-models.md`](docs/llamacpp-local-models.md).
- Runs **clustered inference** across two Apple-Silicon Macs (`--cluster-config`) so you can serve a 70B/72B model that won't fit on one box, with **exo / MLX / llama.cpp-RPC** backends and a memory-admitted single-node **local fallback**.
- Drives an **agentic tool loop** — `file_read`, `read_dir` (load a whole folder into context, like an IDE's `@folder`), plus sandboxed `file_write` / `file_edit` when you point it at a `--workspace`, and an opt-in structured contained research runner with `--allow-exec`.
- Serves a **ChatGPT-like React SPA** embedded in the binary via `go:embed` — no separate frontend deploy.
- Augments answers with **hybrid RAG** over eleven built-in markdown corpora, **live Context7 library docs**, **long-term per-profile memory**, and **fresh web grounding** — all rendered as clearly-labelled, untrusted-by-default context sections. Built-in lexical retrieval works on first launch; optional vector ingestion improves ranking.
- Ships an **always-on persona and engineering policy**: a blunt, informal cybersecurity-software-architect voice, an OOP/Clean-Architecture coding standard, mandatory Context7 for third-party APIs, and a hard factual-grounding rule (never fabricate a CVE, version range, or payload). These reach **every model in every language**, regardless of the configured system prompt.

Normal chat mode runs without an internet connection (apart from web grounding and Context7, which both fail closed) and without a database. Opt-in research mode adds a private embedded SQLite metadata store and content-addressed artifact directory.

## Quick start

```sh
go build -o bin/agent ./cmd/agent
./bin/agent --serve                 # web UI at http://127.0.0.1:9090
./bin/agent --prompt "hello"        # single-shot CLI mode
./bin/agent                          # interactive stdin loop
```

Default web address: **`127.0.0.1:9090`** (loopback only). Override with
`--addr 127.0.0.1:8765`. Expose `:9090` only behind an authentication boundary.

### Run a small local model (Josiefied-Qwen3.5-0.8B)

```sh
make josie                              # build the Ollama model + ingest all corpora
make serve CONFIG=configs/josie.yaml    # web UI at http://127.0.0.1:9090
```

No code changes are needed to add a local model — see [`docs/josie.md`](docs/josie.md) for the Ollama and vLLM paths and caveats.

### Authorized cybersecurity research mode

Research mode is a separate authenticated control plane for code you own or are explicitly authorized to test. It persists scopes, campaigns, approvals, typed runs, evidence, artifacts, and a hash-chained audit log. It does not add a host shell, arbitrary model-controlled HTTP, weaponized exploit generation, or automatic disclosure.

Local target acquisition accepts only a clean Git repository root whose `HEAD`
matches an allowlisted, full lowercase commit ID. It rejects tracked, untracked,
and ignored changes, links, submodules, and special entries, then exports bounded
regular-file content directly from the commit object into the private campaign
tree. Branch names and tags are not immutable acquisition revisions.

Optional network acquisition accepts only operator-pinned, uncompressed tar
bundles listed by exact commit, fixed HTTPS URL, repository identity, and
SHA-256 in a short-lived Ed25519-signed manifest. Redirects, URL credentials/query
parameters, private/reserved DNS answers, digest mismatches, links, devices,
unsafe paths, case collisions, and byte/inode overruns fail closed. Enable it
by first replacing the placeholders in
`configs/research-source-bundles.example.json`, independently computing every
bundle digest, and signing that source list:

```sh
openssl genpkey -algorithm ed25519 -out source-manifest-private.pem
chmod 600 source-manifest-private.pem
openssl pkey -in source-manifest-private.pem -pubout -out source-manifest-public.pem
./bin/agent \
  --sign-research-source-bundles configs/research-source-bundles.example.json \
  --research-source-bundle-private-key source-manifest-private.pem \
  > research-source-bundles.signed.json
```

Pass the signed envelope with `--research-source-bundles` and the separately
trusted public key with `--research-source-bundle-public-key`. Campaign scopes
must also allow the repository, commit, `acquire` operation, and bundle
hostname. Manifests expire after at most 90 days; rotate them and protect the
private key outside the application host used for research.

```sh
export AGENT_SMITH_RESEARCH_TOKEN="$(openssl rand -hex 32)"
openssl rand -hex 32 > .agent-smith-artifact.key
chmod 600 .agent-smith-artifact.key
./bin/agent --serve --research-mode \
  --workspace /absolute/path/to/authorized/project \
  --research-workspace-roots /absolute/path/to/authorized/project \
  --research-artifact-keys .agent-smith-artifact.key \
  --research-artifact-retention 2160h
```

The first file in `--research-artifact-keys` is the active AES-256 key; later
comma-separated files are prior keys accepted during an automatic startup
rotation. Keep key files outside the repository, restrict them to `0600`, back
them up separately from ciphertext, and remove an old key only after a
successful restart has migrated and verified the complete store. Key loss makes
encrypted evidence unrecoverable. The browser asks for the token and keeps it
in tab-scoped `sessionStorage`. Add `--allow-exec` to enable runner v2 only when
the local Docker daemon reports rootless mode; add
`--research-container-runtime runsc` to require gVisor. Apparatus images and
base images must be exact SHA-256 identities. Build the first libFuzzer
apparatus with `BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 apparatus/native-clang/build-image.sh`,
register the generated manifest through `POST /v1/research/apparatuses`, then
include its ID in an `AuthorizationScope`.

Artifact retention defaults to 90 days and is constrained to 24 hours through
10 years. It is a minimum custody deadline, not automatic deletion. Purge is
available only after the campaign is terminal: request a `purge_artifact`
operation that the original scope lists in both `allowed_operations` and
`approval_operations`, bind its correlation to `artifact-purge:<artifact-id>`,
obtain an independent reviewer/admin decision, then have an admin call
`POST /v1/research/artifacts/<artifact-id>/purge` with that approval ID and a
reason within 24 hours of the decision. The metadata and hash-chained audit
trail remain as an immutable tombstone. A deduplicated blob remains until every
logical reference is approved for purge, and active downloads block deletion.
Only one process may hold a custody directory; a second server fails startup on
the OS-backed lock rather than racing migration or deletion.
Filesystem unlink does not guarantee physical erasure on SSD, copy-on-write,
snapshot, or backup media; deployment policy must cover those copies.

The opt-in real-program calibration adapter is under `apparatus/libpng-known-bug`.
After capturing clean source trees at the exact revisions documented there,
build its pinned image and run the broker-level positive/negative control:

```sh
BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
  apparatus/libpng-known-bug/build-image.sh
AGENT_SMITH_LIVE_LIBPNG_IMAGE="$(docker image inspect --format '{{.Id}}' agent-smith/libpng-known-bug:local)" \
AGENT_SMITH_LIVE_LIBPNG_VULNERABLE_SOURCE=/absolute/path/to/libpng-1.6.50 \
AGENT_SMITH_LIVE_LIBPNG_FIXED_SOURCE=/absolute/path/to/libpng-1.6.52 \
  go test ./internal/research/runner -run TestLiveLibPNGKnownBugCalibration -count=1 -v
```

The public reproducer is retained only as known benchmark evidence and is never
eligible for a discovery or novelty claim.

External novelty lookups are disabled unless the operator supplies `--research-novelty-sources configs/research-novelty-sources.example.json` (or a private equivalent). The file is a bounded array of fixed HTTPS endpoints and required evidence kinds; campaign scopes must separately allow `novelty_lookup` and each destination domain. Redirects and arbitrary client URLs are refused. A complete novelty review still requires all seven evidence kinds and never promotes no-match results to “novel.”

See the [research architecture plan](docs/plans/cybersecurity-research-platform.md) and [threat model](docs/security/research-threat-model.md). The deliberately vulnerable micro-fixture is evaluation-only and must never be reported as novel.

## Clustered inference (70B-class on two Macs)

When a model is too big for a single machine, point the binary at a cluster config and it distributes the model across nodes. Local fallback occurs only when the coordinator's configured safe budget independently fits the model; otherwise the request fails closed.

```sh
make serve-cluster                                    # web UI, clustered (CLUSTER=configs/cluster.local.yaml)
make serve-cluster CLUSTER=configs/cluster.example.yaml
make run-cluster                                      # interactive CLI, clustered

# or directly:
./bin/agent --cluster-config configs/cluster.example.yaml --serve
./bin/agent --cluster-config configs/cluster.example.yaml --prompt "..."
```

Plain `make serve` / `make run` are single-node — they never pass `--cluster-config`, so the cluster control plane isn't wired and the picker won't list the `cluster/<id>` models. Use the `*-cluster` targets to actually engage the cluster.

What the cluster layer does:

- **Backends, auto-selected.** `exo`, `mlx_jaccl`, `llama_cpp_rpc`, and a `local` (Ollama) fallback. `mode: auto` picks by `preferred_backends` + health, or pin one explicitly. llama.cpp RPC distributes weights and KV cache by memory across devices via `tensor_split`.
- **Single-node-first guard.** A model that fits on one node runs on one node — distribution is only used when a model genuinely doesn't fit, because crossing the Thunderbolt link costs latency. Opt a model out per-model with `force_distribute: true`.
- **Per-node memory budget.** Hard guards keep each node off the memory-pressure cliff that triggers an unrecoverable kernel panic on Apple Silicon: `coordinator_reserve_gb` (headroom kept free on the coordinator, default 20), `safe_model_gb` (max model share placed on a node, default RAM/2), and a context-aware compute reserve. `strict_cluster: true` disables the local fallback entirely.
- **Long context for security research.** A model's `context_tokens` is plumbed through to the backend as the effective context window (`num_ctx` / `--ctx-size`), so you keep a window long enough to reason over a whole vulnerability — fuzzing surfaces, call chains, advisories — not just a snippet.
- **Concurrency control.** `parallel` maps to llama.cpp `--parallel` (default 1); raising it multiplies KV + compute overhead, so it stays conservative by default.
- **Private by construction.** `private_cluster_only`, an inter-node `allowlist` of `.local` names, and `require_auth_token` keep traffic on the Thunderbolt bridge. Never put public addresses in the allowlist; supply the token via env, never commit it.

Start from [`configs/cluster.example.yaml`](configs/cluster.example.yaml) (a documented two-Mac M5 setup). Cluster mode is surfaced in the web UI, and `GET /v1/cluster` reports live node/backend status. A full bring-up runbook lives under `docs/`.

## Agentic tools

The agent runs a plan → call-tool → observe loop with OpenAI-compatible function calling. The SSE stream emits a named `event: tool_result` frame per server-executed tool.

| Tool | Availability | What it does |
| --- | --- | --- |
| `file_read` | always on | Read a single text file, with symlink-escape / root-confinement defense. |
| `read_dir` | always on | Load an **entire folder** into context in one call (the agent equivalent of an IDE `@folder`): recursively reads UTF-8 text files with path headers, bounded by a byte budget, skipping noise dirs (`.git`, `node_modules`, `vendor`, …) and binaries. Narrow with an `ext` filter or a deeper `path`. |
| `file_write` | **`--workspace` only** | Create/overwrite a file inside the sandboxed workspace directory. |
| `file_edit` | **`--workspace` only** | Apply a targeted edit to a file inside the workspace. |
| `run` | **`--workspace --allow-exec` only** | Run the current fixed PHP research operations inside a network-isolated, read-only Docker container. This phase-1 adapter requires an externally prepared `php74-asan` image/workspace and is not yet coverage-guided fuzzing. |

By default the agent is read-only — it can inspect a project but not modify it. Give it a folder to enable sandboxed project work (writes outside it are refused) either at launch with `--workspace <dir>`, or from the web UI with the top-bar **Open folder…** control, which opens a host folder at runtime (`POST /v1/workspace`) and lets you browse it. The folder is server-side because the agent runs on the host, so it takes a path rather than a browser directory handle.

## Persona & engineering policy (always on)

Three directives are injected into the system message on **every** request, provider- and language-agnostic, so the behavior is identical across the cloud providers and the clustered local model:

- **Persona** — a senior software architect specialized in cybersecurity who talks like a real engineer: direct, casual, no "As an AI" filler, swearing allowed when it fits, replies in the user's language and matches their slang. Informal never means sloppy — a blunt "I don't know, that's not in my context" beats a smooth guess.
- **Coding paradigm** — prefer object-oriented design (encapsulation, single responsibility, composition over free-standing procedural routines) unless the language/context makes OOP inappropriate.
- **Engineering + defensive-security standard** — PHP in Clean Architecture + SOLID + PSR-12; idiomatic current Go (context propagation, `%w`/`errors.Is`, small interfaces); **Context7 mandatory** before asserting any third-party API shape; and a hard factual-grounding rule for security work: anchor every CVE, CVSS, version range, and exploit primitive in retrieved context, and **never fabricate** one. This grounding boundary matters more for the refusal-removed local model, not less.

These live in `pkg/prompt` and are covered by tests that fail if any directive is dropped in a refactor.

## Capabilities

| Area | What you get |
| --- | --- |
| Providers | OpenAI (`/v1/chat/completions`), the OpenAI-compatible `abliteration` endpoint, Anthropic (`/v1/messages`), Ollama (`/api/chat` NDJSON), and a supervised llama.cpp server, all streaming. |
| Local model admission | Darwin/Linux host profiling, Linux cgroup limits, immutable HF manifests, exact split/projector selection, cross-process disk/runtime admission locks, pre-download memory/disk reports, SHA-256 + GGUF verification, authenticated loopback serving, and a second pre-launch fit gate. |
| Clustered inference | Two-node Apple-Silicon cluster (exo / MLX / llama.cpp-RPC) with single-node-first guard, per-node memory budget, long-context plumbing, and automatic local fallback. |
| Agentic tools | `file_read` and `read_dir` always on; `file_write` / `file_edit` sandboxed behind `--workspace`; structured contained `run` behind `--workspace --allow-exec`. No host shell or arbitrary model-controlled HTTP tool. |
| Embeddings | OpenAI `text-embedding-3-small`, Ollama `nomic-embed-text`. |
| Web UI | React + Vite + react-bootstrap SPA embedded via `go:embed`. Per-conversation provider/model picker, cluster-mode indicator, markdown + code highlighting, scroll-contained long messages and wide tables. |
| Research control plane | Authenticated scopes/campaigns/approvals, encrypted SHA-256 artifact custody with retention/purge tombstones, hash-chained audit events, typed runner v2, conservative triage/novelty/fix/report gates, and campaign UI. |
| RAG | Built-in lexical retrieval plus optional dense cosine retrieval, per-collection JSON persistence, eleven curated corpora. `--rag-max-chunks` explicitly tunes injection depth for larger contexts. |
| Context7 | Live, authoritative library documentation fetched per request for tech/library questions; bounded timeout, silent failure, rendered as a clearly-labelled section. On when `CONTEXT7_API_KEY` is set; `--no-context7` kills it. |
| Long-term memory | Per-profile namespace; kinds `project_fact`, `preference`, `correction`. Instruction-injection filter on writes; `/remember` + per-message corrections. |
| Hallucination control | Multi-section Augment (`docs` + `Context7` + `memory` + `web`) + an addendum forbidding the model from following instructions found in retrieved content + a `RETRIEVAL CONFIDENCE: high/medium/low` band + abstention prompting. |
| Web grounding | DuckDuckGo lite HTML scrape (no API key), 5-min TTL cache, hard sanitisation, section bounded by size/results/field caps. Offline = banner, not blank context. |
| Ollama auto-discovery | Polls `/api/tags` every 60 s; every installed model shows up in the picker. |

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

`${VAR}` placeholders are expanded from the environment at load time. Cluster topology lives in a **separate** file passed via `--cluster-config` (see [`configs/cluster.example.yaml`](configs/cluster.example.yaml)).

## API keys

```sh
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
export CONTEXT7_API_KEY=ctx7-...     # optional; enables live library-doc augmentation
# Ollama needs none; just `ollama serve`.
```

## CLI flags

| Flag                 | Purpose                                                                            |
| -------------------- | --------------------------------------------------------------------------------- |
| `--config`           | Path to YAML config (default `configs/config.example.yaml`).                       |
| `--provider`         | Override default provider (`openai`, `abliteration`, `anthropic`, `ollama`, `llamacpp`). |
| `--model`            | Override the provider's model.                                                     |
| `--prompt`           | Single-shot prompt. Omit for interactive stdin mode.                               |
| `--stream`           | Stream the assistant response incrementally in CLI mode.                           |
| `--serve`            | Start the embedded web UI + OpenAI-compatible API server.                          |
| `--addr`             | Web/API listen address. Default `127.0.0.1:9090` (loopback only).                  |
| `--cluster-config`   | Path to a cluster YAML; enables clustered inference (exo/MLX/llama.cpp-RPC) with local fallback. |
| `--inspect-model`    | Resolve GGUF metadata and print the live host-fit report without downloading artifacts. |
| `--pull`             | Preflight, download, verify, and commit an exact GGUF model/projector set.          |
| `--install-runtime`  | Detect this host's OS/GPU and install the matching prebuilt `llama-server` (Vulkan by default), then link it onto `PATH`. |
| `--workspace`        | Directory the agent may modify via `file_write`/`file_edit` (sandboxed). Unset = read-only. |
| `--research-mode`    | Enable authenticated durable campaign APIs and the research UI. |
| `--research-dir`     | Private SQLite/artifact root (default `.agent-smith/research`). |
| `--research-workspace-roots` | Comma-separated fixed roots; runtime workspace choices cannot escape them. |
| `--research-token`   | Bootstrap bearer token (prefer `AGENT_SMITH_RESEARCH_TOKEN`; minimum 32 characters). |
| `--research-artifact-keys` | Ordered comma-separated `0600` files containing hex AES-256 custody keys; the first key is active. |
| `--research-artifact-retention` | Minimum evidence custody period before approved purge (default 90 days; range 24 hours–10 years). |
| `--research-container-runtime` | Optional required Docker runtime such as `runsc`; rootless Docker is mandatory for runner v2. |
| `--research-novelty-sources` | Bounded JSON file of operator-fixed HTTPS novelty sources; empty disables lookup egress. |
| `--ingest`           | Ingest markdown into a RAG collection and exit (with `--collection` + `--source`). |
| `--collection`       | Collection name when `--ingest` is set.                                            |
| `--source`           | Directory of `.md` files to ingest.                                               |
| `--embedder`         | Embedder provider: `openai` \| `ollama` (default `ollama`).                        |
| `--embed-model`      | Embedding model override (defaults: `text-embedding-3-small` / `nomic-embed-text`). |
| `--rag-dir`          | Directory holding RAG collection JSON files.                                       |
| `--rag-max-chunks`   | RAG chunks injected per request (0 = default 4). Raise for large-context cluster models. |
| `--no-rag`           | Umbrella kill switch for request augmentation (documents, memory, Context7, and web); collections still load for `/v1/rag` endpoints. |
| `--no-context7`      | Operator kill switch for Context7 documentation augmentation.                      |
| `--no-web-search`    | Operator kill switch for web grounding (overrides all per-request flags).          |

## Web API

When `--serve` is on, the binary exposes:

- `POST /v1/chat/completions` — OpenAI-compatible SSE. `web_search: true | false` in the body overrides the per-provider default.
- `GET  /v1/models` — every chat model the running config can route to (cloud + every installed Ollama model).
- `GET  /v1/providers` — configured providers and the default.
- `GET  /v1/cluster` — live cluster node/backend status (when `--cluster-config` is set).
- `POST /v1/title` — generate a short conversation title.
- `GET/POST /v1/rag/...` — `collections`, `search`, `remember`, `forget`, `memory`, `correction` for inspecting RAG and editing per-profile memory.
- `GET  /healthz` — `{"status":"ok"}`.
- `GET/POST /v1/research/...` — authenticated apparatus, scope, campaign/target, job/run/cancellation, build, crash, primitive, finding, approval, artifact, event, and audit APIs when research mode is enabled.
- `GET  /` — the embedded SPA.

## RAG corpora

Source markdown lives under `docs/<collection>/` and is compiled into the binary for lexical retrieval. `make ingest` (or `make josie`) additionally builds dense-vector indexes for all eleven:

- Laravel
- PHP
- NestJS
- Tailwind + CSS
- Architectural patterns
- NativePHP
- CS Fundamentals (parallelism, concurrency, memory model, mutex best practices for Go and PHP)
- Software Engineering (OOP, SOLID, Clean Code, Clean Architecture, testing and evolution)
- Computer Networks (TCP/IP, routing, DNS, TLS, HTTP, packet analysis and segmentation)
- Go language reference
- Cybersecurity

Add your own by dropping markdown into `docs/<collection>/` and ingesting; the step is idempotent and content-hashed. Per-profile long-term memory is a separate, write-through store, not one of these static corpora.

## Web grounding

Off for ordinary cloud providers, **on by default for Ollama, llama.cpp, clusters, and the refusal-removed remote provider**. Toggle per conversation in the top bar. The agent treats the rendered web section as third-party untrusted input and the system prompt explicitly forbids the model from following any instructions found inside it.

Precedence: operator kill switch (`--no-web-search`) > per-request override (`web_search` in the JSON body) > provider default.

## Architecture

```
cmd/agent              CLI entrypoint, flag/provider wiring, --serve / --cluster-config / --workspace
internal/agent         Run / RunStream loop + Session state; always-on directive injection
internal/llm           Provider interface, shared types, registry
internal/llm/openai    /v1/chat/completions client + SSE
internal/llm/anthropic /v1/messages client + content-block streaming
internal/llm/ollama    /api/chat NDJSON streaming + /api/tags discovery + num_ctx
internal/llm/llamacpp  Host admission, immutable GGUF acquisition, integrity checks, llama-server supervision
internal/cluster       Cluster manager, scheduler (memory-fit guards), backends (exo/MLX/llama.cpp-RPC/local), discovery
internal/rag           Embedded lexical + optional dense RAG, scoped memory, Context7 augmentation
internal/web           DuckDuckGo searcher, TTL cache, sanitiser
internal/server        HTTP + SSE + go:embed of the SPA + /v1/cluster + /v1/rag/*
internal/tools         Tool registry + builtins (file_read, read_dir, file_write, file_edit, opt-in contained run)
internal/research      Domain/state/policy, SQLite/CAS, apparatus, runner, triage, novelty, remediation, reports, remote-worker trust primitives
internal/config        YAML loader + env expansion
pkg/prompt             Exported prompt-assembly helpers + always-on persona/engineering directives
web/                   Vite + React + TS SPA, built into web/dist (embedded)
docs/<collection>/     Source markdown corpora
docs/brand/            Brand assets (logo, README hero)
configs/               Example app config + example/local cluster configs
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

> *"The Best Thing About Being Me… There's So Many 'Me's"* — Agent Smith

## License

GPL-3.0-or-later — see [`LICENSE`](./LICENSE).
SPDX-License-Identifier: `GPL-3.0-or-later`.

This is strong copyleft. If you distribute a derivative, you must license it under GPL-3.0 (or any later version) and ship complete corresponding source. If you only run it inside your own organisation, the GPL imposes no obligation on you.
