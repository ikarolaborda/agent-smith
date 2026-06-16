# ADR 0001 — Native llama.cpp RPC inference in Go: feasibility

Status: **Accepted** (recommendation: keep subprocess orchestration)
Date: 2026-06-16
Decision drivers: a request to bring llama.cpp RPC tensor-split inference
"into the Go app" at **same-or-better performance**.

## Question

Can agent-smith replace its current design — Go supervising `llama-server`
(coordinator) + `rpc-server` (worker) — with a **native-Go** implementation of
distributed tensor-split inference, at equal or better performance?

## Evidence

- **Cross-machine tensor-split is a separate-process protocol** (Tier-3,
  ggml-org/llama.cpp via Context7): `rpc-server` is a standalone executable that
  "exposes ggml devices on a remote host"; the RPC backend "communicates with one
  or several instances of `rpc-server` and offloads computations." The worker side
  is therefore inherently a separate process — it cannot become an in-process Go
  path without reimplementing the ggml-RPC wire protocol **and** the device
  backends.
- **Performance is the Metal/ggml kernels.** Decode throughput is dominated by
  llama.cpp's hand-tuned Metal (and SIMD) kernels. A pure-Go reimplementation has
  no Metal kernels; it would be dramatically slower. There is no production-grade
  pure-Go GGUF+Metal inference engine — Ollama, LM Studio, etc. all wrap llama.cpp
  via cgo.
- **The original mandate** for this work was explicitly *"do not implement tensor
  parallelism in Go; keep Go as the control plane."*

## Options

| Option | Feasible? | Performance | Cost |
| --- | --- | --- | --- |
| **A. Native pure-Go** reimpl of llama+RPC+Metal | No (realistically) | **Far worse** (no Metal kernels) | Enormous; contradicts the mandate |
| **B. cgo-embed `libllama`** on the coordinator | Yes | **Equal**, never better (same kernels) | Breaks pure-Go single static binary; Metal/Apple-framework linking; cross-compile pain; in-process crash blast radius; **worker `rpc-server` still required** |
| **C. Keep subprocess orchestration** (current) | Yes — already shipped | llama.cpp's best | None new; Go stays a pure control plane |

## Decision

**Keep Option C as the default and recommended architecture.** The "same/better
performance in native Go" gate is not met (Option A is defeated on performance
and scope), so the "if possible, implement it" condition does not trigger for the
native interpretation. Option B is the only equal-performance alternative, but it
buys only the removal of the *local* `llama-server` hop while sacrificing the
pure-Go single-binary identity and **still** requiring the worker `rpc-server`.

A further point favors C: running `libllama` in-process (Option B) **increases
blast radius** — a model/allocator/backend fault takes down the Go process,
whereas today a supervised child can crash and be restarted (process group +
backoff already implemented) without killing the agent.

## If Option B is still wanted (optional, future spike)

Scope it narrowly behind a build tag (e.g. `//go:build llama_cgo`):
- Goal: remove the local `llama-server` subprocess on the coordinator only — **not**
  a performance feature.
- Keep the pure-Go build the default distribution artifact; cgo path is opt-in,
  macOS-specific, operationally heavier.
- Success criteria before adopting: decode tok/s within measurement noise of
  `llama-server`; no regression in RPC tensor-split; acceptable startup/shutdown
  and packaging/signing on macOS; benchmark matrix (prompt tok/s, decode tok/s,
  startup latency, memory, failure recovery) vs Option C on the same hardware.
- Rollback: the existing `llama_cpp_rpc` backend stays the fallback.

## Consequence

No native-Go inference rewrite. `internal/cluster/backend_llamacpp.go`
(supervise `llama-server --rpc … --tensor-split`) remains the implementation;
the worker continues to run `rpc-server`. Revisit only if a credible pure-Go
GPU/Metal backend emerges or project constraints change radically.
