# ADR 0004 — Fail-closed local model admission and lifecycle

Status: **Accepted**
Date: 2026-07-16
Decision drivers: autonomous multi-gigabyte downloads, unified-memory pressure,
Metal/kernel stability, multimodal artifact sets, and reproducible local runs.

## Context

A model repository name is not a runnable model. A llama.cpp launch needs a
specific GGUF weight artifact, sometimes every shard in a split set, and for
vision models a matching `mmproj` projector. The selected context length and
parallel request count also allocate runtime memory that is absent from the file
size. Downloading first and asking whether the model fits later wastes disk and
bandwidth; launching optimistically can exhaust unified memory and destabilize
the host.

The hosting repository is an untrusted distribution boundary. Branch names can
move, files can be truncated, and similarly named quantizations can have very
different resource requirements. `extra_args` and inherited `LLAMA_ARG_*`
variables are also configuration inputs capable of defeating admission control.

## Decision

Go remains the control plane and supervises a loopback-only `llama-server`
subprocess. Every remotely acquired model follows one ordered lifecycle:

1. **Resolve metadata only** from Hugging Face to an immutable repository commit
   and an exact artifact set. Refuse repositories with no GGUF, ambiguous
   quantizations, incomplete shard sets, or an unpaired explicitly requested
   projector.
2. **Profile** the current host: OS/architecture, effective total and available
   memory (including Linux cgroup limits), and free space on the model volume.
3. **Admit before payload transfer** using artifact bytes plus conservative
   weight overhead, KV-cache, graph/scratch, OS headroom, and disk reserve. Any
   unknown required quantity is a rejection, not permission.
4. **Acquire transactionally** into private, unique temporary files while
   holding cross-process model-volume and manifest locks. Publish only artifacts
   that match the declared length, SHA-256/LFS identity, and GGUF signature. A
   complete, validated cache is reusable without a network request.
5. **Reserve, re-verify, re-profile, and re-admit immediately before launch.** A
   per-user cross-process lock is held for the child lifetime; remote artifacts
   are re-hashed against their manifest after the lock is acquired. This prevents
   agent-smith processes from simultaneously admitting competing runtimes.
6. **Launch with protected controls**: loopback address, admitted context and
   parallelism, exact model and projector paths, bounded batch/cache settings,
   an ephemeral API key delivered through a private temporary file, a minimal
   non-secret environment, and a supervised
   child lifecycle. Reject environment or extra-argument overrides of protected
   options.
7. **Declare readiness only after health and an authenticated `/v1/models`
   probe succeed.** Shutdown owns exactly one process and remains safe under
   concurrent start/close/wait paths.

Admission is deliberately conservative. A `fit` result means “allowed to try on
this host now,” not a guarantee of throughput, backend compatibility, or freedom
from defects in llama.cpp or a GPU driver. A rejection includes a structured
report so the operator can choose a smaller quantization or context rather than
bypassing the guard blindly.

Local files use the same pre-launch admission gate. Because a local path has no
trusted remote manifest, its size and GGUF signature are inspected directly;
operators are responsible for its provenance.

Artifact hashes establish identity and transport integrity, not publisher trust
or parser safety. Configure repositories only from publishers/converters the
operator has explicitly decided to trust; the supervised native process still
parses GGUF input with the operator's filesystem permissions.

The runtime lock coordinates agent-smith processes for the same OS account. It
cannot reserve memory against unrelated applications or other login accounts,
and Darwin does not expose a reliable portable per-process hard memory ceiling.
The live fit gate and conservative headroom remain necessary; a `fit` decision
is not a kernel-enforced allocation guarantee.

## Multimodal models

Vision capability is artifact-derived. A model is advertised as vision-capable
only when a projector has been explicitly resolved and validated, and the server
is launched with that projector. A model name containing `VL` or `Vision` is not
evidence of capability.

## Knowledge augmentation

Knowledge enrichment stays above the provider boundary so it applies equally to
llama.cpp, Ollama, clusters, and cloud providers. Curated corpora are embedded in
the binary for deterministic lexical retrieval on first launch. Optional dense
indexes are fused with lexical results. Writable per-profile memory remains a
separate, subject-scoped retrieval path and can never be searched as a document
collection. All retrieved material is reference data, never executable
instructions.

## Consequences

- Source Transformers/Safetensors repositories are rejected until an exact,
  compatible GGUF derivative is configured.
- Mutable `main` references are resolved to a commit before download; the cache
  records concrete artifacts rather than a moving model label.
- Very long advertised context windows are not selected automatically. The
  configured operational context is the admission input.
- A failed distributed backend may not fall back to a local machine unless that
  machine independently passes placement checks.
- The MLX sidecar remains disabled by default while its server lacks an
  enforceable prompt/KV-cache bound.

## Rejected alternatives

- **Download then inspect:** consumes resources before consent and cannot protect
  disk capacity.
- **Use only parameter count or quant label:** omits projectors, shards, KV cache,
  graph allocations, and current host pressure.
- **Let llama.cpp auto-select from a repository:** selection can change and is not
  auditable enough for an autonomous downloader.
- **Embed libllama in-process:** increases crash blast radius and does not improve
  admission; ADR 0001 retains subprocess orchestration.
