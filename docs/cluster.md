# Clusterized inference

agent-smith can run inference across more than one machine while keeping the Go
binary as a pure **control plane**. The Go process never does tensor math: it
discovers nodes, launches and supervises an external inference runtime, routes
chat requests, streams tokens, records metrics, and fails over. The heavy
lifting (tensor/pipeline parallelism, RDMA, sharding) is done by the data-plane
runtime you choose.

Every backend ends up as an **OpenAI-compatible HTTP endpoint on the
coordinator's loopback**, which the existing `internal/llm/openai` client drives.
The whole cluster is exposed to the rest of the app as one `llm.Provider` named
`cluster`, so nothing in the agent loop, server, or UI had to change.

## Backends (priority order)

| Backend | Mode key | What it is | Status |
| --- | --- | --- | --- |
| exo | `exo` | exo auto-discovers Apple-cluster peers and serves an OpenAI/Ollama-compatible API. Go connects/launches and proxies. | preferred |
| MLX/JACCL | `mlx_jaccl` | Python sidecar (`scripts/mlx_sidecar.py`) wrapping `mlx_lm.server`; distributed via `mlx.launch` over Thunderbolt. | supported |
| llama.cpp RPC | `llama_cpp_rpc` | `llama-server --rpc host:port,…` fanning out to `rpc-server` workers. | **experimental, private network only** |
| local | `local` | The existing single-node runner (Ollama/OpenAI/Anthropic). Always the fallback. | always available |

The scheduler picks the first backend (in the model's `preferred_backends`) that
is installed, fits the model in available memory, and comes up healthy. If none
do, it falls back to `local` — unless `runtime.strict_cluster: true`.

## Hardware assumed by the example

- **Node A — coordinator:** MacBook Pro M5 Max, 40 GPU cores, 64 GB. Host `m5max.local`.
- **Node B — worker:** MacBook Pro M5 Pro, 20 GPU cores, 24 GB. Host `m5pro.local`.
- Thunderbolt 5 bridge between them; the two `.local` names resolve over it.

Config: [`configs/cluster.example.yaml`](../configs/cluster.example.yaml). Point
`models[].path` at your own quantized 70B/72B weights — no model name is baked
into the agent.

## Build

```sh
go build -o bin/agent ./cmd/agent
```

## Security defaults

- APIs bind `127.0.0.1` by default; `private_cluster_only: true` rejects a
  non-loopback `bind_host`.
- Inter-node traffic is restricted to the `allowlist` (or, if empty, the
  configured node hosts). Non-private hosts are refused under
  `private_cluster_only` and warned about for llama.cpp RPC (RPC is
  unauthenticated — keep it on the Thunderbolt/LAN link only).
- Request/output logging is **off** unless `runtime.log_prompts: true`.

---

## Mode 1 — single node (no cluster)

Nothing new; this is the existing behavior. Omit `--cluster-config`:

```sh
./bin/agent --serve --config configs/josie.yaml
./bin/agent --prompt "review this diff for injection bugs" --config configs/config.example.yaml
```

You can also get the cluster wrapper with only the coordinator node and
`mode: local` — useful to keep metrics/fallback semantics on one machine.

## Mode 2 — exo cluster (preferred)

On **both** Macs, install and start exo (it auto-discovers peers over the
Thunderbolt link):

```sh
# Node A (m5max) and Node B (m5pro):
exo
```

Then on the **coordinator** point agent-smith at exo. Either let it launch exo
or set `runtime.exo.endpoint` to the running service:

```sh
# configs/cluster.example.yaml -> cluster.mode: auto (or exo), runtime.exo.endpoint optional
./bin/agent --cluster-config configs/cluster.example.yaml --serve
# CLI:
./bin/agent --cluster-config configs/cluster.example.yaml --prompt "find vulns in ./internal"
```

The provider proxies to exo's `/v1/chat/completions` with SSE streaming, health
checks, and retry/fallback.

## Mode 3 — MLX/JACCL cluster

Requires MLX + mlx-lm on both nodes and a working distributed transport
(Thunderbolt ring/RDMA on supported macOS):

```sh
pip install mlx mlx-lm        # on both nodes
```

Set the model `path` to an MLX model directory (or HF id) and run on the
coordinator:

```sh
./bin/agent --cluster-config configs/cluster.example.yaml --serve
# with cluster.mode: mlx_jaccl   (or auto with mlx_jaccl ahead of llama_cpp_rpc)
```

What happens:

1. Go writes a JSON hostfile from the node config (`{"ssh": "...", "slots": N}`).
2. Go launches `scripts/mlx_sidecar.py` with `MLX_METAL_FAST_SYNCH=1` (when
   `runtime.mlx.fast_sync: true`).
3. The sidecar serves `mlx_lm.server` on `127.0.0.1:8081`. For multiple nodes it
   re-launches under `mlx.launch --hostfile …`. **If `mlx.launch` or the
   RDMA/ring transport is missing, the sidecar prints a clear diagnostic to the
   logs and degrades to single-host serving instead of failing silently.**
4. Go proxies chat to the sidecar endpoint.

Pipeline parallelism instead of tensor split: `runtime.mlx.pipeline: true`.

## Mode 4 — llama.cpp RPC (experimental, private only)

Build llama.cpp with RPC on **both** nodes:

```sh
cmake -B build -DGGML_RPC=ON -DGGML_METAL=ON
cmake --build build --config Release
```

On the **worker** (Node B) start the RPC server bound to the private interface:

```sh
# m5pro.local — keep this on the Thunderbolt/LAN address, never 0.0.0.0 on a public NIC
./build/bin/rpc-server -H 0.0.0.0 -p 50052
```

On the **coordinator** (Node A) run agent-smith; it launches `llama-server` with
`--rpc m5pro.local:50052`, `-ngl 99`, and the configured `--tensor-split`:

```sh
./bin/agent --cluster-config configs/cluster.example.yaml --serve
# cluster.mode: llama_cpp_rpc   (or auto)
```

`models[].path` must be a `.gguf` file on the coordinator. The `~64:24` memory
ratio maps to `tensor_split: "0.73,0.27"`. agent-smith **warns** if any RPC host
is not on a private interface.

---

## Verifying & metrics

Health and the latest metrics (TTFT, tokens/sec, prompt/generated tokens,
backend selected, node reachability, memory pressure, process restarts) are
tracked by the in-process metrics collector and emitted on each completed
request in the logs, e.g.:

```
cluster: request complete backend=exo ttft_ms=412 tokens_per_sec=27 generated_tokens=512 ...
```

Quick sanity check that routing + fallback works without any runtime installed:

```sh
go test ./internal/cluster/...
```

## RAG, memory, and augmented context in cluster mode

These are **fully preserved** in cluster serve mode. RAG retrieval, long-term
per-profile memory, web grounding, and Context7 augmentation live in the *agent*
layer (`agent.Agent.RAG` → `Augment`), which wraps whatever `llm.Provider` is
selected. The cluster is just another provider, so a request routed to `cluster`
gets the identical augmentation pipeline as `ollama`/`openai`:

- **RAG over the curated corpora** — unchanged; applied before the prompt reaches the cluster backend.
- **Long-term memory** (`/remember`, corrections, per-profile recall) — unchanged.
- **Web grounding** — defaults **ON** for `cluster` (it serves local models, like Ollama), suppressing hallucination. Operator `--no-web-search` and the per-request flag still take precedence.
- **Context7 docs** — unchanged; injected whenever `CONTEXT7_API_KEY` is set.

CLI mode (`--prompt` / interactive) has no RAG by design — that was true before the cluster work and is unchanged.

## Failure behavior

- A backend process that exits is restarted per `process_restart_policy`
  (`on_failure`, up to `max_restart_attempts`); restarts are counted in metrics.
- If the chosen cluster backend can't be brought up, the scheduler tries the
  next preferred backend, then falls back to `local`.
- With `strict_cluster: true`, a failure to bring up a cluster backend is a hard
  error (no local fallback).

## Hardening (residual risks closed)

- **No post-commit replay:** backend selection (and any cross-backend fallback)
  happens *before* streaming. Once the chosen backend emits its first token the
  request is committed — a mid-stream failure surfaces as an error, never a
  silent replay on another backend.
- **Process-group cleanup:** supervised children run in their own process group
  (`Setpgid`) and Stop signals the whole group, so the MLX sidecar's
  `mlx.launch` child tree is reaped instead of orphaned.
- **Restart backoff:** crashes restart with exponential backoff + jitter (1s→30s)
  bounded by `max_restart_attempts`, so a bad model path can't spin a tight
  relaunch loop.
- **Private-only, resolved:** under `private_cluster_only`, a node host that
  *resolves* to a public IP is refused — not just string-checked. DNS resolution
  is best-effort so a legitimately offline/private setup is never broken.
- **Probe robustness:** health probing tries `/v1/models`, then `/health`, then
  the server root, tolerating per-backend endpoint differences.
- **Unreachable-worker signal:** starting a distributed backend logs a prominent
  warning for each worker discovery marked unreachable (no silent single-node
  degradation).

## Assumptions / accepted design

- Worker-node runtimes (exo on Node B, `rpc-server` on Node B) are started by the
  operator (or your own launchd/ssh wrapper); the coordinator-side process is
  fully managed by agent-smith. This is an accepted ops decision, now with an
  explicit unreachable-worker warning. SSH remote launch remains a clean
  extension point on the supervisor.
- `transport_preference` is recorded and surfaced to backends (MLX hostfile,
  llama.cpp interface warnings) rather than implementing a transport stack in Go.
- Memory-pressure telemetry is **observability-only**: it is reported as
  `MemoryPressureUnknown` (-1) for nodes without a metrics agent and is **never**
  used in placement — the scheduler fits models against the statically configured
  `memory_gb` of reachable nodes only.
- exo CLI specifics can vary by version; agent-smith treats exo purely as an
  OpenAI-compatible endpoint and does not depend on niche flags.
