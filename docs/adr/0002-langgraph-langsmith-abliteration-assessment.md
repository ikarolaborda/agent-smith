# ADR 0002 — LangGraph, LangSmith, and abliteration.ai: fit & gains assessment

Status: Proposed (assessment, not yet implemented)
Date: 2026-06-17
Deciders: project owner
Related: [ADR 0001](0001-llama-cpp-native-in-go-feasibility.md), `internal/llm`, `internal/agent`, `internal/cluster`

## Context

We want to raise confidence in agent-smith's cybersecurity research. Three additions were proposed: the **abliteration.ai** hosted model (`ABLITERATION_AI_API_KEY`), **LangGraph** (stateful graph orchestration), and **LangSmith** (tracing/observability, `LANGSMITH_API_KEY`).

The product's core thesis matters more than any single feature: **single-binary, offline-first Go**, no database, all grounding/augmentation (RAG + long-term memory + web + Context7) in the Go agent layer wrapping any `llm.Provider`, used for **private** vuln research with local **abliterated** models where grounding is the safety boundary. The real question is not "can we?" but "do these erode the offline/private boundary, and is the gain worth it?"

### Verified facts (Tier-3 / authoritative)

- **abliteration.ai is OpenAI-compatible**: base URL `https://api.abliteration.ai/v1`, `/v1/chat/completions`, streaming + function calling + structured output, "switch base URL + key". It is a **remote, hosted** endpoint. (abliteration.ai docs.)
- **LangGraph is officially Python / JS-TS only.** Go has only community reimplementations (`golanggraph`, `langgraph-go`, etc.) and an official *remote API client* to LangGraph Platform — **no official in-process Go graph engine.** (LangChain docs + pkg.go.dev survey.)
- **LangSmith can trace non-LangChain apps via OpenTelemetry** — OTLP endpoint `https://api.smith.langchain.com/otel`, header `x-api-key=<key>` (needs `langsmith>=0.4.25` on the Python path); there is also an **alpha** official `github.com/langchain-ai/langsmith-go`. So observability is reachable from Go **without** LangChain/LangGraph, and via **vendor-neutral OTEL** it is reachable without LangSmith at all. LangSmith is **cloud SaaS** (self-hosted option exists). (LangChain "trace with OpenTelemetry" docs.)
- **agent-smith integration surface**: `openai.Config` already has `BaseURL`; providers self-register via `init()`; config blocks carry `api_key`/`base_url`/`model`. There is **no tracing/OTEL** today (only `slog` + the cluster metrics `Collector`).

## Decision (recommendation)

| Proposal | Verdict | Why |
| --- | --- | --- |
| **abliteration.ai provider** | **Adopt — gated as a remote provider** | Near-trivial (reuse the OpenAI transport). Real gain *if* you want that hosted model. But it is **remote inference** → breaks strict-offline; must be opt-in with an egress warning, not silently part of the core path. |
| **LangGraph (the library)** | **Reject as a dependency** | Python/JS only → a Python sidecar/second runtime that breaks the single-binary offline thesis; community Go ports are immature/single-maintainer supply-chain risk for a security tool. Build the *orchestration pattern* natively in Go instead. |
| **LangSmith (cloud) tracing** | **Don't wire the cloud SaaS into the core** | Cloud egress of prompts, retrieved exploit data, file/shell outputs conflicts with private research. Instead instrument **vendor-neutral OTEL**, default-OFF, with redaction; LangSmith becomes one optional OTLP backend (prefer self-hosted). |

The framing that drives all three: **product-boundary drift.** Each addition is fine *only* if isolated behind opt-in config / build flags / runtime policy so a user cannot accidentally turn an offline, private tool into a remote, telemetered one.

## Where the real "extra confidence" gain is

Not LangGraph-the-library, but the *pattern* it represents. The current loop is a single `plan → tool → observe`. A **native Go orchestration layer** over the existing `llm.Provider` + tools + augmentation interfaces gives the actual win, with zero new runtime:

- bounded **critic / self-correction loops** (draft → critique → revise, capped),
- **conditional branches** and **subtask delegation** (e.g. a "grounding-check" pass that must cite RAG/Context7 before a CVE claim stands — aligns with the existing anti-hallucination posture),
- explicit, typed **state transitions** (small Go state machines / graph structs compiled into the binary; persistence optional + file-backed, never a DB).

Scope it narrowly to concrete confidence-improving patterns; do **not** build a generic graph DSL until repeated use cases justify it.

## Consequences / implementation sketch (if pursued)

1. **Remote-provider gating** (prerequisite for abliteration.ai or any hosted model): add a `network`/egress policy — e.g. `allow_remote_providers: false` default + a startup banner when prompts may leave the host. Document abliteration.ai as **incompatible with strict offline mode**.
2. **abliteration.ai**: a thin named preset over the OpenAI-compatible path (base URL + `ABLITERATION_AI_API_KEY`), reusing the current chat/tool/stream transport. **Before claiming low risk, run a conformance suite**: tool-calling schema, streaming delta semantics, finish reasons, error payloads, cancellation/timeouts, malformed tool args. "OpenAI-compatible at the happy path" ≠ identical.
3. **Native orchestration**: a small package over `agent.Run` for bounded retry/critic/conditional/subtask execution using existing provider/tool/memory interfaces.
4. **Observability**: OTEL spans around agent turns, provider calls, tool invocations, retrieval, retries; keep the existing metrics `Collector`. **Off by default**, with redaction modes (`metadata-only` / `redacted-content` / `full-content`). Export targets local-first (stdout/file/Jaeger/Tempo/local collector); LangSmith via OTLP only as an optional backend, self-hosted preferred for sensitive work.
5. **Docs/UX guardrails**: clearly separate `offline/local`, `remote inference`, and `remote telemetry` modes so the product thesis can't be violated by accident.

## Risks

- abliteration.ai may diverge from OpenAI on tools/streaming/finish-reasons/errors despite happy-path compatibility → subtle runtime bugs if assumptions are hard-coded.
- Any remote provider/telemetry blurs the offline/private guarantee unless the egress boundary is explicit in config + docs.
- A Python LangGraph sidecar adds packaging burden, attack surface, and failure modes, weakening the single-binary story even when "optional".
- Native orchestration can sprawl into an overengineered framework if not tightly scoped.
- OTEL/LangSmith tracing can leak highly sensitive research artifacts if redaction is incomplete; even self-hosted introduces retention/access-control obligations.

## Evidence tiers

- abliteration.ai compatibility, LangGraph language support, LangSmith OTEL ingestion: **Tier-3** (official/vendor docs + web). Conformance of abliteration.ai's tool/stream behavior with our code: **Tier-1 not yet run** — gated behind the conformance suite above.
- agent-smith integration surface (`BaseURL`, registry, no OTEL today): **Tier-1** (source on disk).
- Adversarial design review: **buddy** `01KVA87D7RTQGJ6SC2BP95FQMN` — accepted, high confidence.
