# Cluster RDMA-over-Thunderbolt (via Exo) + dense-70B model feasibility

Status: assessment (2026-06-17). Verdict: **the operator's correction is right** — RDMA on
this cluster is an **Exo + macOS 26.2 + Thunderbolt 5** capability, not a llama.cpp feature.

## TL;DR
- RDMA over Thunderbolt 5 is real on Apple Silicon as of **macOS 26.2**; **Exo supports it day-0**.
- We have been running `mode: llama_cpp_rpc`, whose RPC backend **cannot use RDMA on Apple**
  (`GGML_RPC_RDMA` is force-disabled on macOS — that path is Linux/RoCE-only). That earlier
  finding was correct *for llama.cpp* but was the wrong framework for this question.
- To benefit from RDMA we must **switch the cluster to the Exo backend** and **enable RDMA**.

## Findings (verified)
| Claim | Tier | Evidence |
|---|---|---|
| RDMA over TB5 needs macOS 26.2+, TB5 Macs + TB5 cables; enable via `rdma_ctl enable` in Recovery mode + reboot | 3 | Exo official README (context7 /exo-explore/exo) |
| Exo uses MLX backend + tensor parallelism (~1.8x on 2 devices, ~3.2x on 4) | 3 | Exo README / bench methodology |
| Coordinator (M5 Max) is **macOS 26.5.1** (≥ 26.2 → qualifies) | 2 | `sw_vers` |
| Coordinator has a **Thunderbolt 5** link (80 Gb/s, "up to 120 Gb/s"; TB4 is 40) | 2 | `system_profiler SPThunderboltDataType` |
| **exo is installed** at `~/.local/bin/exo` | 2 | `which exo` (config comment "exo isn't installed" is now stale) |
| llama.cpp RPC RDMA is force-OFF on Apple | 3 | llama.cpp `ggml/src/ggml-rpc/CMakeLists.txt` |

## Why "RDMA disabled on both nodes" is expected (not a misconfiguration to debug)
`rdma_ctl enable` is a **manual, Recovery-mode, per-machine** step that has not been run, and the
active backend (llama.cpp RPC) can't use RDMA anyway. So the disabled state is the default, on both.

## What RDMA does — and does NOT — do (important, no overclaim)
- DOES: collapse inter-device latency over TB5 (Exo cites ~99% latency reduction) → faster
  tensor-parallel sharding, better TTFT/throughput.
- Does NOT: add memory to the 24 GB worker, pool unified memory, or raise its weight-residency
  limit. The 3 prior kernel panics are **consistent with memory / Metal working-set pressure**
  (dense weight residency) per the config notes — an informed attribution, not log-proven this
  session. RDMA speeds the link; the worker's physical memory ceiling is unchanged. Feasibility of
  any dense 70B split still hinges on the per-node memory share, and **tensor parallelism still needs
  substantial per-shard memory plus runtime KV/activation/staging overhead — a nominal 42–45 GB
  model size alone does NOT prove the 24 GB worker is safe.**

## Enable runbook (operator — physical steps, cannot be automated from here)
1. Confirm BOTH Macs are macOS 26.2+ (coordinator = 26.5.1 ✓; **verify the M5 Pro worker**).
2. Use a **TB5-certified cable** directly between the two Macs.
3. On EACH Mac: shut down → boot into **Recovery mode** → Terminal → `rdma_ctl enable` → reboot.
4. (MacBook Pros here — Mac Studio port restrictions don't apply, but heed Exo's port notes.)
5. Run `exo` on both nodes; it auto-discovers peers and elects a master.

## CORRECTION (2026-06-17): the `exo` on PATH is the WRONG tool
`/Users/ikarolaborda/.local/bin/exo` (v0.1.2) is **"a collection of command-line utilities for the
Yeast Epigenome Project"** — a bioinformatics CLI, a **name collision**, NOT exo-explore/exo. The AI
cluster Exo is therefore **not installed**. Do NOT `pip install exo` (that's the collision). Install
the real one from source (Tier-3, exo README):
```bash
git clone https://github.com/exo-explore/exo && cd exo
cd dashboard && npm install && npm run build && cd ..
uv run exo            # prereqs: Xcode, Homebrew, uv, Node.js, Rust nightly, macmon (pinned rev)
```
So "move to Exo" has THREE operator-gated prerequisites before any bench: (1) install real exo from
source on both Macs, (2) `rdma_ctl enable` in Recovery mode + reboot on both, (3) TB5 cable. Only
then is the staged bench (`scripts/exo-staged-bench.sh`) runnable.

## Migrating the cluster from llama.cpp RPC → Exo
- `configs/cluster.local.yaml`: set `mode: exo` (NOT `auto` — auto's exo-probe previously hung;
  pin it now that exo is installed). Exo exposes an OpenAI-compatible endpoint (default :52415),
  which our `internal/cluster/backend_exo.go` already targets.
- Keep llama.cpp RPC as the documented fallback (`strict_cluster: false` already falls back to local).

## Dense-70B model feasibility (DeepSeek-R1-Distill-Llama-70B-abliterated, Llama-3.3-70B-Instruct-abliterated)
Both are **dense 70B**; Q4_K_M ≈ 42–45 GB (verified: mradermacher Llama-3.3-70B-abliterated-v2
Q4_K_M = 42.5 GB; confirm exact repo/file/size on the HF Files tab before download — do not assume).
- Under **llama.cpp RPC** these are the proven panic path (dense weight residency crashed the 24 GB
  worker 3×). Under **Exo tensor-parallel + MLX + RDMA**, sharding a dense model across two Macs is
  the *intended* use case and is materially more promising — but the 24 GB worker memory ceiling
  still gates it. **Open question (needs a live Exo bench):** does the worker's tensor-parallel
  shard + KV + compute buffer fit ≤ ~18 GB? Use Exo's `/bench/chat/completions` `peak_memory_usage`
  per node to settle it before committing.
- Honest priority note: **model choice is not the binding constraint** for the 0-day campaign
  (the execution/feedback loop is — now built; and the fuzzing apparatus is currently blocked by a
  UBSan-at-startup, see below). A stronger 70B sharpens hypotheses/triage; it does not raise the
  evidence tier.

## Campaign status (separate, honest)
The PHP-7.4 ASan/UBSan apparatus currently **aborts at PHP startup** (UBSan: non-zero offset on
null pointer in `mbstring.c:784` during INI registration) on *every* run, masking all fuzzing — a
benign startup UB, not a vulnerability. Fixing this (e.g. `UBSAN_OPTIONS=halt_on_error=0` or
suppressing the known startup UB) + seeding the empty `exif` corpus is the real next step toward a
crash — independent of the cluster/model work. No 0-day is claimed.

## Evidence still required (before any durable "70B is viable" conclusion)
- Worker (M5 Pro) **actual** macOS version ≥ 26.2 and TB5 link confirmed.
- `rdma_ctl enable` applied on BOTH hosts with successful reboot; TB5-certified cable.
- A **staged Exo bench**: (1) a smaller dense model known to fit, to isolate Exo correctness from
  size pressure; (2) the target 70B quant; (3) if feasible, RDMA-on vs RDMA-off comparison.
  Capture per node: load success/failure, `peak_memory_usage`, tokens/sec, first-token latency,
  sustained-generation stability, and whether any failure is at load vs decode.

## Next checks (highest information gain first)
1. Verify the **worker** macOS version (≥26.2) and TB5 link.
2. After `rdma_ctl enable` on both, run the staged Exo 2-node bench above and read `peak_memory_usage`.
3. Fix the apparatus UBSan-at-startup (separate blocker thread — does NOT bear on cluster feasibility)
   so fuzzing actually runs.
