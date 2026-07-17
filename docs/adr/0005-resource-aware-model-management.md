# ADR 0005 — Resource-aware model management

Status: **Accepted (phase 1 implemented)**
Date: 2026-07-17

## Context

The llama.cpp provider could download and run a GGUF model, but two things were
manual and one was impossible:

1. **Manual tuning.** `gpu_layers` and `ctx_size` had to be hand-edited per host.
2. **No GPU support off Apple.** `runtime.go` hard-rejected any `gpu_layers > 0`
   on a non-Apple host ("discrete-GPU VRAM admission is not implemented"), and
   `HostProfile` had no GPU fields, so a Linux box with an AMD/NVIDIA card ran
   CPU-only.
3. **No in-app way to discover/download/run models** — you edited YAML.

The concrete target that motivated this: an AMD **RX 7800 XT** (16 GB VRAM,
RDNA3 = `gfx1101`, which is in llama.cpp's official ROCm prebuilt targets),
128 GB RAM, 8 TB disk, Linux Mint.

## Decision

### Phase 1 (implemented)

- **GPU detection** (`internal/llm/llamacpp/gpu.go`). Best-effort, fail-open to
  `none`. Linux: `nvidia-smi` (CUDA + VRAM), `rocm-smi` (ROCm + VRAM), then
  `lspci` + a Vulkan capability probe for identification. Darwin: Metal, unified
  memory. `HostProfile.GPU` carries `{vendor, name, vram_bytes, backend,
  unified}`.
- **Auto-tune** (`tune.go`, `RecommendRuntime`). From the detected host + model
  artifact sizes it picks `gpu_layers` and `ctx_size`: full offload (`-ngl 99`)
  with the largest context that fits when the accelerator has room; partial
  offload proportional to VRAM otherwise; CPU/RAM-bounded when there is no GPU.
  Wired at config-assembly (`cmd/agent/main.go`): when the operator sets neither
  `gpu_layers` nor `ctx_size`, the app tunes automatically on launch. Explicit
  config always wins.
- **VRAM admission** (`fit.go`). A discrete-GPU full offload must fit
  weights + KV in the VRAM budget (device memory minus a driver/compute
  reserve). The system-RAM gate still applies to host-side scratch. This
  replaced the blanket non-Apple refusal.
- **Endpoints**: `GET /v1/system` (host + GPU), `GET /v1/models/search?q=`
  (bounded Hugging Face GGUF catalog passthrough).
- **UI**: a "Models & system" panel (top-bar button) showing detected hardware
  and a Hugging Face GGUF search.

### Phase 2 (designed, not yet implemented)

**One-click download & run from the UI.** The provider is currently built once
at boot. To load a browsed model without a restart:

- A `ModelManager` owning the lifecycle of a `llamacpp.Runtime`, able to
  `Start`/`Stop`/`Replace` under a lock, and to register/replace the provider in
  `server.providers` (which is read-only after `New` today — this becomes a
  guarded, swappable slot).
- `POST /v1/models/pull` — SSE download progress (the downloader already streams
  byte counts; surface them as events), gated by the fit report.
- `POST /v1/models/activate` — stop the current runtime, start the requested
  one, swap the provider, update `/v1/models`.
- `POST /v1/models/inspect` — resolve a HF manifest + fit + `RecommendRuntime`
  for a preview before download (reuses `Downloader.Inspect`).
- UI: per-result fit badge ("fits: full offload, ctx 16384"), a download button
  with a progress bar, and a run button.

Risks to handle in phase 2: concurrent activate requests (serialize), a failed
start leaving no active provider (keep the previous one until the new one is
ready), and disk/VRAM churn from repeated pulls.

## Consequences

- The RX 7800 XT (and any CUDA/ROCm/Vulkan GPU) is now usable, and a fresh
  install auto-configures to the host instead of needing hand-tuned YAML.
- Detection is Tier-2-verified on macOS (Metal) via unit-tested parsers for the
  Linux tool outputs; the **real AMD/ROCm path is unrun on the dev machine** and
  should be smoke-tested on the target box (`rocm-smi`/`nvidia-smi` present,
  `-ngl 99` offload confirmed in the llama-server banner).
