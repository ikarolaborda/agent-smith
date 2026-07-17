# Memory-readiness assessment: freeing the most RAM for local LLMs, safely

Status: assessment (pre-ADR). Author: engineering. Scope: how to build a
mechanism that frees the highest possible amount of memory **without killing any
system-essential process**, so local models load and run with the best chat UX.

Target hosts: macOS/arm64 (Metal, unified memory) dev box; Linux/amd64 + AMD
RX 7800 XT (ROCm) production box.

---

## TL;DR

The naive reading of the goal — "aggressively purge OS caches and close other
processes to create free bytes" — is the wrong mechanism, and partly a
falsified one. The right mechanism is a **layered memory-readiness pipeline**
that (1) frees what *we* own first, (2) shrinks the model's runtime footprint to
fit, (3) accounts for reclaimable memory accurately, (4) retries in a free
window, and only then (5) *advises* the user about other processes — never
auto-killing anything.

The single highest-value, universally-safe lever is a **single-model-at-a-time
invariant**: tear down our own prior `llama-server` (and release our RAG/index
caches) *before* admitting the next model. It frees the largest block of memory
we actually control, on every platform, with zero risk to other processes.

---

## Why "purge caches / kill processes" is the wrong frame

Two grounded facts defeat the naive approach:

- **Linux already counts reclaimable cache as available.** `host.go` reads
  `/proc/meminfo` `MemAvailable`, which the kernel computes to *include*
  reclaimable page cache. Running `drop_caches` evicts exactly the pages the
  admission gate already treats as available — so it yields ≈0 net admission
  gain while cold-starting every cache. (Tier‑3: kernel `MemAvailable`
  semantics; Tier‑1: `internal/llm/llamacpp/host.go:126` `linuxMemory`.)
- **Killing processes violates the hard constraint** and risks data loss even
  for non-essential user apps. The only safe form of "reclaim from other
  processes" is *advisory*: surface the hogs, let the user decide.

So the mechanism is not an OOM-killer. It is a readiness pipeline whose job is to
**see, reclaim-what-we-own, shrink-to-fit, retry, and advise.**

---

## What the code does today (Tier-1 findings)

- **Admission gate** — `fit.go:EstimateFitWithPolicy` is fail-closed and pure.
  Budget = `min(Total − osReserve, Available − headroom)` where
  `osReserve = max(4 GiB, Total/8)` and `headroom = max(1 GiB, Total/32)`. On a
  loaded box the `Available − headroom` term binds. Runtime estimate =
  `artifacts + 15% weight overhead + scratch(max(1 GiB, artifacts/10)) + KV`,
  with `KV = ctx × parallel × 512 KiB/token`.
- **Host profile** — `host.go`. Linux uses `MemAvailable` (reclaim-inclusive,
  good). macOS `darwinMemory` = `(Pages free + inactive + speculative) ×
  pagesize`. cgroup limits already tighten the Linux numbers.
- **Runtime** — `runtime.go` launches `llama-server` with **mmap ON** (no
  `--no-mmap`) and **mlock OFF**; both are reserved (denylisted) passthrough
  flags. `Close` is a `SIGINT → SIGKILL` process-group teardown via `sync.Once`.
- **Tuner** — `tune.go:RecommendRuntime` already ladders `ctx` down to the
  largest that fits and scales `gpu_layers` to VRAM. Applied only when both
  `gpu_layers` and `ctx_size` are unset.

## Measured on this box (Tier-2, `vm_stat` + `memory_pressure`, 24 GiB Mac)

`free + inactive + speculative ≈ 9.2 GiB` — and that *already* includes the big
reclaimable **inactive** bucket. The genuine undercount is only **purgeable
(~0.3 GiB)** plus a fraction of active file-backed cache. So "fix macOS
accounting" is real but **modest**, not the dominant lever. The same measurement
shows the more interesting fact: with mmap on, ~5.5 GB of qwythos weights are
**file-backed evictable pages**, yet the fit estimate counts them as committed —
i.e. the gate is conservative by roughly the weight size.

## Confirmed lever (Tier-3, llama.cpp docs via context7)

`-ctk/--cache-type-k` and `-ctv/--cache-type-v` default to `f16`; allowed values
include `q8_0` (≈½ the KV bytes) and `q4_0` (≈¼). `--mmap` is on by default;
`--mlock` forces keep-in-RAM (we correctly do **not** set it). KV-cache
quantization is therefore a real, first-class way to shrink the runtime footprint
enough to admit a model or a larger context.

---

## The mechanism: a staged admission pipeline

Ordered by safety × value. Each stage runs only if the previous one still leaves
the model rejected. Reject only after the whole ladder is exhausted, and always
with a human-readable explanation of *what* was tried.

**Stage 0 — Instrument first.** Log pre/post memory at every stage, which stage
admitted the load, retry outcomes, and any swap/pressure distress. Nothing below
is trustworthy without this.

**Stage 1 — Self-reclamation (primary lever, all platforms).**
Formalize a **single-model-at-a-time invariant** in `runtime.go`: before
admitting a new model, `Close()` any existing `llama-server`, *wait for actual
process exit*, and release agent-owned caches (RAG in-memory index, any warm
buffers). Then **re-measure** the host. This frees the largest block we control
and turns a stale rejection into an admission when the previous model's RSS was
the thing in the way. Must be carefully synchronized — fit decisions must not
race delayed process exit or delayed page-cache reclaim (use the existing
`waitDone` exit signal).

**Stage 2 — Self-memory accounting in `fit.go`.**
Let the gate subtract memory that agent-owned teardown *will* reclaim from the
pessimistic current-state estimate, so we never reject on memory that is about to
disappear. Additive: a new optional `ReclaimableSelfBytes` input, credited only
for resources we can prove we own and will free.

**Stage 3 — Shrink-to-fit ladder (existing `tune.go` + new KV quant).**
When still rejected: walk the `tune.go` ctx ladder down, then apply KV-cache
quantization `-ctk q8_0 -ctv q8_0` (then `q4_0` only if policy allows), then
reduce GPU offload before ctx on VRAM-bound hosts. Surface it: "running at q8_0
KV / 8k ctx to fit." Product policy must set a **minimum acceptable ctx** and
whether `q4_0` KV is allowed, so we prefer a *stable smaller* config over a
*barely-admitted* one.

**Stage 4 — Pressure-aware bounded retry.**
Read Linux `/proc/pressure/memory` (PSI) and macOS `memory_pressure`/`vm_stat`
heuristics; retry admission once or twice over a short, clearly-surfaced window
to catch a free moment. (Episodic evidence: a bounded retry once caught a free
window during the qwythos bring-up.) Must be tightly bounded and shown in the UX
so it never reads as a hang.

**Stage 5 — Accurate-reclaimable accounting (small win).**
Add macOS **purgeable** pages to available (no cgo needed via `vm_stat`); keep
Linux `MemAvailable` as-is. Low complexity budget — the measured gain is ~0.3 GiB
here.

**Stage 6 — User-consented "Free memory" action (never automatic).**
A UI action that by default reclaims **only agent-owned** resources. It may
*also* list the top user-owned memory consumers and offer optional OS-specific
commands (macOS `purge`, Linux per-cgroup `memory.reclaim`) **behind explicit
consent** — but it never auto-closes or auto-kills any external process, and
never touches anything system-essential.

**Experimental / gated — mmap-evictable admission credit.**
*Not* a default ranked lever (buddy modification, adopted). Optimistically
crediting mmap'd weight pages as "available" risks thrash, swap growth, and cold
faults — worst on unified-memory Macs under desktop load. If introduced at all:
cap the credit conservatively, and gate it behind low-pressure + no-swap-distress
health checks and a "warm reload" heuristic (limited credit only for a model
whose weights are already in page cache).

**Anti-thrash protections (cross-cutting).**
Cooldown after a failed load; detect repeated load→evict→reload cycles; always
prefer a smaller stable configuration over a larger unstable one.

---

## UX

- A **memory gauge** in the Models/chat panel (extends `/v1/system`): total,
  available, reclaimable-by-us, and what the next model needs.
- A **"preparing memory…"** state during Stage 1 teardown + re-measure.
- **Transparent fit explanation** reusing `FitReport.Reasons`: what was tried
  (self-reclaim, KV quant, ctx ladder, retry) and why the final decision stands.
- **Graceful right-size fallback** messaging instead of a dead-end rejection.

---

## Output contract (what is verified vs not)

**Findings (Tier 1–3, verified):** current gate/host/runtime behavior; mmap
on/mlock off; macOS available already counts inactive; Linux `MemAvailable` is
reclaim-inclusive so `drop_caches` gives ≈0 admission gain; llama.cpp KV
quantization and mmap/mlock semantics.

**Supported hypotheses (need implementation + measurement):** the exact
self-reclaim credit; the macOS purgeable/active-file coefficient; how much a
bounded retry actually helps in practice; the safe cap for any mmap credit.

**Checks run:** read `fit.go`/`host.go`/`runtime.go`/`tune.go`; `vm_stat` +
`memory_pressure` on the dev Mac; context7 on llama.cpp memory flags.

**Checks NOT run:** no Linux/ROCm host measurement (PSI, cgroup `memory.reclaim`,
VRAM pressure) — the RX 7800 XT box is the place to validate Stages 4–5 and
GPU-aware admission; no end-to-end thrash/swap-storm replay; no empirical
quality cost of `q8_0`/`q4_0` KV or reduced ctx on tool-call reliability.

**Next best check:** implement Stage 0 instrumentation + Stage 1 self-reclamation
behind the existing seams, then measure pre/post admission on both the Mac and
the ROCm box near the fit threshold.

**Residual risk:** KV-quant/ctx reduction can degrade quality or tool-call
reliability; retries add perceived latency if not bounded/surfaced; GPU-memory
observability is weaker and driver-specific across Metal vs ROCm; self-reclaim
must be synchronized against delayed exit / delayed page-cache reclaim.

---

## Seam map (where each change lands, additive)

- `host.go` — measurement: macOS purgeable; Linux PSI + optional VRAM pressure.
- `fit.go` — policy: `ReclaimableSelfBytes` credit; staged decision with reasons.
- `tune.go` — fallback ladder: KV-quant option alongside the ctx ladder.
- `runtime.go` — orchestration: single-model teardown, wait-for-exit, re-measure,
  bounded retry, anti-thrash cooldown.
- `internal/server/system.go` + `ModelExplorer.tsx` — the gauge, "preparing
  memory" state, and the user-consented "Free memory" action.
