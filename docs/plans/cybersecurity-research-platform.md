# Cybersecurity research and exploit-primitive discovery plan

- Status: Implemented baseline; beta release gates outstanding
- Assessment date: 2026-07-19
- Scope: defensive, authorized source-code vulnerability research
- Supersedes: none; extends ADR 0003 beyond its phase-1 contained runner

## Executive decision

Agent Smith is currently a capable local-first security **copilot**, but it is
not yet a research platform and it is not an autonomous exploit-primitive
finder. Its strongest foundations are provider abstraction, a tested agent/tool
loop, grounding, local inference, and an opt-in contained execution proof of
concept. Its binding gaps are deterministic campaign orchestration, a real
coverage-guided apparatus, durable typed evidence, artifact custody, enforced
authorization/approval policy, and benchmark-driven evaluation.

Do not expand the generic chat loop into an unrestricted shell. Build a durable
research control plane around it. The model should propose hypotheses, harnesses,
and explanations; deterministic workers should build and run targets; parsers and
policy gates should decide which evidence states have actually been earned.

The first useful product milestone is deliberately narrower than “find a
zero-day”: given a pinned, authorized target containing a known memory-safety
bug, the application can build the target, run a real coverage-guided fuzzer,
preserve and hash the crashing input, reproduce and minimize it, symbolize the
trace, deduplicate the root cause, characterize the demonstrated primitive,
propose a fix and regression test, and produce an evidence-complete report. It
must never promote the known fixture as novel.

## Implementation outcome (2026-07-19)

Phases 0 and 1 are implemented. The repository now has truthful capability
reporting, authenticated research APIs, fixed operator roots, durable SQLite
campaign/job/evidence state, a private SHA-256 artifact store, approvals, and a
hash-chained audit journal. Phase 2's typed broker, restart recovery,
cancellation, concurrency control, hostile artifact collector, exact-image
Docker backend, rootless/seccomp/cgroup preflight, and remote signed-envelope
primitives are implemented; production enablement still requires a host that
passes the rootless Docker or gVisor preflight and enforces disk/inode quotas.

The native Clang apparatus implements the closed operation vocabulary and has
been live-tested with real libFuzzer/ASan against a vulnerable fixture and a
clean twin. The API evidence pipeline now performs immutable local source
capture, build materialization, fuzz crash ingestion, three independent replay
gates, bounded deletion minimization, offline symbolization, crash grouping,
and conservative primitive/finding creation. Every unmeasured primitive field
remains `unknown`. Authenticated chat agents receive one principal-bound,
read-only `research_query` tool that returns bounded campaign metadata and
opaque evidence IDs; it cannot return artifact bytes or host storage paths. The
post-triage workflow now retains machine revision comparisons or explicit
reviewer-owned untested reasons, fixed-source novelty responses and immutable
reviews, approved candidate diffs, patch-linked builds, three-part runtime
remediation validation, private reports, and separately approved human
disclosure records. No-match novelty results remain `novelty_unverified`.

The implementation is not beta-ready. A clean stochastic real-program
discovery/minimization campaign, end-to-end real-program branch/novelty/fix
validation, constrained network acquisition, kernel/filesystem disk and inode
enforcement, encrypted retention operations, and an independent security review
remain release blockers. The current host reports neither rootless Docker nor
gVisor, so the production CLI correctly refuses to start its research runner
here; rootful Docker results are functional lab evidence only.

The runner now treats CPU seconds and inode count as required finite scope,
campaign, manifest, and job ceilings. Docker applies a CFS CPU rate derived from
the CPU/wall envelope and process/file rlimits; the broker monitors writable
output and corpus trees, records peak byte/inode growth, rejects hostile entry
types, and cancels overruns. This does not replace the kernel/filesystem quota
release gate, and capability reporting keeps kernel storage quotas and complete
CPU/RSS accounting false.

An opt-in hostile containment apparatus now exercises network denial, read-only
mounts, control-plane secret isolation, cross-campaign visibility, device
creation, orphan cleanup, hostile symlink output, and writable byte/inode
exhaustion through the real Docker backend. All nine controls passed on the
assessment host with the exact local image ID
`sha256:1758e9e26b768681fc41e46bf8e324f36826a69e4cf88b431ac5c24c6caf4f27`.
This is reproducible rootful-lab evidence, not production isolation evidence.

A second opt-in apparatus now calibrates the real broker against public libpng
CVE-2025-64720 evidence. It pins vulnerable 1.6.50 commit
`2b978915d82377df13fcbb1fb56660195ded868a`, fixed 1.6.52 commit
`fbed16182b92eeb3a06d96e49f0836d450318098`, a digest-pinned offline Clang 14
image, an independently hashed 1x1 PNG seed, and the separately retained public
reproducer. The live test builds both exact source trees, requires the
vulnerable build to emit the ASan global out-of-bounds read in
`png_image_read_composite`, requires the independent seed to remain clean on
the vulnerable build, and requires the fixed build to remain clean. It passed
through the real rootful lab backend with image ID
`sha256:3dd0b529373b49c9cf6bcb083da81d79b8696f62872314f74aba19518885ef3e`.
This proves build/reproduction/negative-control calibration, not clean-corpus
autonomous discovery, minimization, novelty, or production isolation.

## Operating boundary

This plan supports research on code the operator owns or is explicitly
authorized to test. It supports minimal, evidence-driven characterization of a
bug's primitive—for example, an attacker-influenced out-of-bounds read or a
fixed-value out-of-bounds write. It does not automate weaponized exploit chains,
credential access, persistence, evasion, indiscriminate scanning, targeting of
third-party deployments, public disclosure, or publication of reusable
weaponized proof-of-concept material.

An `AuthorizationScope` is a required machine-enforced campaign input, not a
sentence in a prompt. It identifies the permitted repository/revision, operator,
purpose, expiry, permitted operations, resource budget, network policy, and
disclosure contact. Expired, absent, or mismatched scope stops execution.

## Baseline architecture assessment (before implementation)

| Area | Current implementation | Readiness | Assessment |
| --- | --- | --- | --- |
| Model and inference | Provider-neutral OpenAI/Anthropic/Ollama/llama.cpp interface, streaming, local/cluster runtimes | Strong foundation | Keep behind the existing `llm.Provider` boundary. Model quality is not the primary blocker. |
| Agent orchestration | Bounded chat-completion loop that dispatches model-selected tools and feeds textual results back | Copilot-grade | Useful for interactive reasoning, but not a durable workflow engine. There is no campaign state machine, scheduler, retry policy, checkpoint, or resumable job. |
| Grounding | Embedded lexical corpus, optional dense RAG, Context7, web snippets, profile memory, prompt-injection framing | Useful but indirect | Good for methodology and explanation. Retrieval confidence is not evidence confidence, and retrieved prose cannot confirm a runtime finding. |
| Answer validation | Optional NVD lookup, optional independent model reviews, optional LLM judge/refinement | Advisory only | Verifiers are opt-in and fail-soft. NVD validates a CVE record, not product applicability or novelty. Model review cannot promote a finding. |
| File tools | Workspace-confined read/write/edit tools with path hardening | Useful | Suitable for generating harness/fix candidates after an approval layer is added. Runtime workspace selection is currently too permissive for a network-exposed server. |
| Advertised `shell` / `http` tools | Registered definitions whose `Execute` methods return `not implemented` | Missing | README/tool descriptions overstate capability. Remove the placeholders from the registry or expose capability status truthfully. Do not implement a host shell. |
| Contained runner | Opt-in structured `run` tool using hardened `docker run` flags, fixed operations, network off, read-only root, limits, process-tree cleanup | Good proof of concept | The boundary is materially better than host exec, but it still trusts the Docker daemon/kernel and default seccomp. Digest pinning is optional. Rootless/stronger isolation is not verified at startup. |
| Research apparatus | Hard-coded `php74-asan`, PHP driver/corpus paths, and `fuzz/reproduce/triage` verbs | Incomplete | The repository contains no apparatus Dockerfile, `scripts/build.sh`, harnesses, or corpus. `fuzz` invokes PHP over a corpus path; it is not a coverage-guided fuzzer. `build` is refused. |
| Artifact flow | Captured stdout/stderr rendered into a string; `/work` is read-only and `/tmp` is ephemeral | Missing | Crashing inputs, minimized reproducers, corpora, coverage, binaries, and symbol files cannot be durably exported by the runner. The ADR's typed result schema is only partially implemented and is lost at the `Tool` string boundary. |
| Research persistence | Chat session in memory; web conversations in browser `localStorage`; RAG collections as JSON | Missing | There is no server-side campaign, run, finding, evidence, approval, artifact, or disclosure store. Browser chat is not chain of custody. |
| Security boundary | Loopback default and CORS checks; no application authentication; workspace can be changed through the API | Insufficient for research mode | CORS is not authorization. An exposed instance could select host directories and invoke mutating tools. Research mode needs authentication, role checks, fixed root allowlists, approvals, and auditable actor identity. |
| Evaluation | Good Go unit/integration coverage and RAG fixtures; containment argv/process tests | Application tests pass | There is no end-to-end known-bug benchmark, harness-quality metric, finding precision/recall measure, artifact-integrity test, or claim-calibration evaluation. |
| User experience | Chat-centric React SPA with streamed tool cards and local conversations | Chat-grade | Research needs campaigns, target/build/run status, coverage, crash groups, artifacts, approvals, findings, and report views. Chat should attach to a campaign, not be its database. |

### Important drift to correct first

1. The README says `shell` and `http` work; the registered implementations are
   placeholders. A model wastes iterations calling tools that cannot succeed.
2. ADR 0003 references an apparatus `scripts/build.sh` and `php74-asan` assets
   that are absent from this repository.
3. ADR 0003 describes structured result ingestion as phase 2, but the public
   tool result is still an unversioned string and does not contain artifact
   paths or CPU/memory/disk accounting.
4. The runner's only persistent host mount is read-only. That is safe for source
   inspection, but incompatible with corpus evolution, crash preservation,
   minimization, coverage, or compiled-output export.
5. The Dockerfile uses Go 1.23, `go.mod` declares Go 1.24, and contributing docs
   claim Go 1.26+. Toolchain truth needs one source of authority.

## Target architecture

```text
Web / terminal / API
        |
        v
Identity + authorization scope + approval policy
        |
        v
Research control plane (durable state machine)
  |          |                |                 |
  |          |                |                 +--> report/disclosure drafts
  |          |                +--> deterministic evidence/claim gates
  |          +--> planner/analyst LLM (proposes; never promotes evidence)
  +--> job scheduler and event journal
        |
        v
Runner broker ------------------------------------------------+
  | build worker | fuzz worker | replay/minimize | analysis    |
  | isolated, no credentials, network denied by default       |
  +------------------------------------------------------------+
        |
        v
Artifact ingestion -> content-addressed artifact store + metadata database
        |                         |
        |                         +--> parser/dedup/symbolization/coverage
        v
Explicit egress broker -> pinned source/advisory/upstream-history lookups
```

Keep the Go binary as the control plane if the single-binary goal remains
important, but treat research workers as hostile disposable workloads. The
orchestrator must not perform target builds or execute target code in-process.

### Trust boundaries

1. **Control plane:** authenticated API, policy engine, state machine, metadata,
   event journal, and LLM/provider adapters. It holds no target runtime code.
2. **Acquisition/build:** fetches only approved sources through a constrained
   broker, resolves an immutable revision, builds in an isolated worker, and
   emits provenance. It does not share model/provider credentials.
3. **Execution:** runs instrumented target binaries with no network or secrets,
   read-only target/build inputs, a per-run writable corpus, and a per-run
   writable artifact directory.
4. **Ingestion:** treats every filename, log, stack frame, archive, and report as
   hostile input; caps size/count/depth, rejects symlinks and device files,
   hashes content, and copies only allowlisted artifact types into storage.
5. **Egress:** novelty/advisory/upstream-history clients use fixed protocols,
   allowlisted destinations, response caps, caching, and provenance. The model
   never receives an arbitrary network request primitive.

## Core domain model

Use versioned domain types rather than chat strings. A minimal first schema is:

- `AuthorizationScope`: scope ID, operator, target constraints, allowed
  operations, network policy, resource ceilings, expiry, disclosure owner.
- `Campaign`: scope, target, goal, status, budgets, timestamps, active plan.
- `TargetRevision`: repository identity, immutable commit/tag resolution,
  source hash, language/toolchain, supported-branch matrix.
- `ApparatusManifest`: adapter version, builder image digest, executor runtime,
  build variants, sanitizers, harnesses, seed/dictionary inputs, limits.
- `Build`: exact inputs, commands selected from an adapter, toolchain versions,
  environment allowlist, output artifact references, logs, provenance, status.
- `ExperimentRun`: operation, deterministic typed arguments, worker identity,
  start/end, exit/termination, resource usage, coverage summary, artifact refs.
- `Artifact`: content hash, media type, size, origin run, logical role,
  sensitivity, retention, storage location, transformation lineage.
- `CrashObservation`: sanitizer/class, signal, symbolized frames, input artifact,
  build, determinism result, signature, suspected security relevance.
- `CrashGroup`: stable root-cause bucket with member observations and canonical
  minimized reproducer.
- `PrimitiveAssessment`: operation demonstrated, attacker control, read/write
  size and value/range control, target-object relationship, repeatability,
  reachability, mitigations, gaps, and evidence references.
- `Finding`: label, evidence tier, affected revisions, root cause, CWE,
  novelty/supported-branch gates, fix, regression, human review state.
- `Approval` and `AuditEvent`: actor, action, policy decision, reason,
  correlation ID, timestamp, and immutable event-chain reference.

Store metadata in an embedded transactional database with migrations (SQLite is
the current preferred direction in the repository roadmap). Store large files in
a private content-addressed directory. Database rows reference SHA-256 content
IDs; artifact bytes never live in chat history. Encrypt or place the store on an
encrypted volume when campaigns contain embargoed findings.

### Typed runner result

Replace `Tool.Execute(...) (string, error)` for research operations with a
versioned internal result, rendered to text only at the final provider boundary:

```json
{
  "schema_version": 1,
  "run_id": "run_...",
  "operation": "fuzz",
  "status": "completed|failed|cancelled|timed_out",
  "exit": {"code": 0, "signal": "", "reason": ""},
  "output": {
    "stdout_artifact": "sha256:...",
    "stderr_artifact": "sha256:...",
    "stdout_truncated": false,
    "stderr_truncated": false,
    "bytes_dropped": 0
  },
  "artifacts": [
    {"role": "crashing_input", "content_id": "sha256:...", "size": 123}
  ],
  "resource_usage": {
    "wall_ms": 1000,
    "cpu_ms": 700,
    "max_rss_bytes": 123456,
    "disk_written_bytes": 1234
  },
  "apparatus": {
    "image_digest": "sha256:...",
    "target_revision": "...",
    "harness": "...",
    "sanitizer": "address"
  },
  "audit_correlation_id": "audit_..."
}
```

The provider receives a bounded summary plus opaque IDs it can use through
read-only research query tools. Raw multi-megabyte logs and binary inputs should
never be inserted wholesale into model context.

## Research workflow and evidence state machine

The workflow is resumable and each transition has a deterministic guard:

1. `draft -> scoped`: valid, unexpired authorization and target match.
2. `scoped -> acquired`: immutable revision and source hash recorded.
3. `acquired -> build_ready`: builder image is content-pinned; build provenance
   and required sanitizer/harness outputs exist.
4. `build_ready -> fuzzing`: harness smoke tests and seed corpus validation pass.
5. `fuzzing -> crash_observed`: a saved artifact and machine-parsed crash signal
   exist. A timeout, OOM, assertion, or bare UBSan message is a distinct
   observation class, not silently a memory-safety finding.
6. `crash_observed -> reproduced`: the same content-addressed input reproduces
   under the same build enough times to meet campaign policy.
7. `reproduced -> minimized`: the minimized artifact reproduces the same stable
   crash signature and is no larger than the original.
8. `minimized -> root_caused`: symbolized frames, source revision, offending
   location, violated invariant, and root-cause group exist.
9. `root_caused -> primitive_assessed`: attacker-control and primitive fields are
   individually backed by referenced evidence; unknown stays unknown.
10. `primitive_assessed -> branch_checked`: configured supported revisions were
    tested or explicitly marked untested with reasons.
11. `branch_checked -> novelty_reviewed`: NVD plus vendor advisories, upstream
    history/blame, issue tracker, changelog, regression corpora, and duplicate
    root-cause checks are recorded. “Not found” remains “novelty unverified.”
12. `novelty_reviewed -> remediated`: candidate fix compiles and the original
    reproducer plus regression suite pass without suppressing the signal.
13. `remediated -> report_ready`: evidence completeness and internal consistency
    checks pass; a human reviewer approves the wording.
14. `report_ready -> disclosed`: a human performs the external action. The
    application only prepares a private draft and records the decision.

Only the state machine may change these statuses. The LLM may request a
transition, but cannot write `reproduced`, `confirmed`, `affected`, `novel`, or
`disclosed` directly.

## Exploit-primitive assessment contract

The application should characterize the smallest demonstrated capability, not
infer a complete exploit from a crash. A `PrimitiveAssessment` records:

- trust-boundary input and how much of it the operator controls;
- operation: crash, out-of-bounds read/write, use-after-free, type confusion,
  invalid free, arithmetic-to-memory effect, control-data influence, or other;
- access width/count and whether it is fixed, bounded, or input-controlled;
- value control for writes and disclosure content/control for reads;
- target relationship: allocator slack, same object, adjacent object, metadata,
  code pointer, unknown;
- repeatability across allocator/build/architecture variants;
- reachability and required privileges/configuration;
- relevant mitigations and the exact observed effect of each;
- explicit gaps between the demonstrated primitive and code execution,
  information disclosure, privilege change, or cross-boundary impact;
- content-addressed evidence for every non-unknown field.

Suggested labels are monotonic:

`hypothesis` -> `observation` -> `crash observed` -> `reproduced memory-safety issue`
-> `primitive candidate` -> `primitive confirmed` -> `candidate vulnerability`.

`Primitive confirmed` means only that the recorded low-level capability is
reproduced and evidence-backed. It does not mean exploitable, novel, CVE-worthy,
or a zero-day. Those are separate gates.

## Runner and apparatus design

### Runner abstraction

Introduce a `Runner` interface implemented by isolated backends rather than
embedding Docker command construction in a chat tool. Initial backends:

- `docker-rootless`: minimum supported Linux posture; preflight must verify the
  daemon is rootless, cgroup limits are active, the expected seccomp profile is
  active, and the image digest is exact.
- `gvisor`: preferred stronger isolation where `runsc` is installed and the
  target/toolchain is compatible.
- `remote-worker`: later, for a dedicated disposable fuzzing host or private CI.

Docker Desktop and rootful Docker can remain an explicitly lower-assurance lab
profile, never silently equivalent to rootless or gVisor.

Every run uses:

- immutable, digest-pinned image and adapter version;
- non-root UID and user namespace where available;
- no credentials and a synthetic home;
- no network by default;
- read-only build/source inputs;
- separate writable corpus and output volumes scoped to one campaign/run;
- capability drop, no-new-privileges, seccomp, LSM profile where supported;
- CPU, memory, PIDs, wall time, output, file-size, inode, and disk-write limits;
- process/container cleanup on cancellation and service restart;
- an artifact collector that rejects symlinks, devices, sockets, deep archives,
  oversized files, and unexpected paths before host ingestion.

### Apparatus adapters

Use a manifest-driven adapter shape similar to mature fuzzing infrastructure:

- target metadata and immutable source revision;
- builder Dockerfile/image definition;
- build recipe that emits fuzz targets into a declared output directory;
- named harnesses, engines, sanitizers, architectures, dictionaries, options,
  and seed corpus archives;
- smoke-test and reproduction entrypoints;
- symbol and source-map outputs needed for offline symbolization.

The first native-code adapter should support real libFuzzer-compatible targets
and these structured operations:

`inspect`, `build`, `list_harnesses`, `smoke_test`, `seed`, `fuzz`, `reproduce`,
`minimize`, `merge_corpus`, `coverage`, `symbolize`, `compare_revision`, and
`regression_test`.

Do not let the model provide commands. It chooses an operation and typed values
validated against both the campaign manifest and adapter schema.

### Acquisition and egress

Separate networked acquisition from offline build and execution:

1. The operator approves repository and destination domains.
2. A constrained fetcher resolves a revision, records upstream identity,
   downloads with response/size/time caps, and writes a source bundle.
3. The bundle is hashed and becomes the only source input to offline workers.
4. Dependencies are pinned, cached, and represented in build provenance.
5. Novelty lookups go through dedicated clients; no arbitrary `http` tool is
   required for the model.

## APIs and user experience

Add server-side research APIs under `/v1/research`:

- campaigns and authorization scopes;
- target revisions and apparatus manifests;
- builds, runs, cancellation, and resumable event streams;
- artifacts and safe textual previews;
- crash observations/groups and primitive assessments;
- approvals, audit events, findings, fixes, and report drafts.

Use typed SSE events such as `campaign_state`, `run_started`, `run_progress`,
`artifact_ingested`, `crash_observed`, `approval_required`, and `finding_updated`.
The existing `tool_result` string remains for ordinary chat compatibility.

The web application needs a research workspace with:

- campaign list and scope/expiry banner;
- target/build provenance and apparatus health;
- run queue, resource usage, coverage trend, and cancellation;
- crash groups with reproducer/minimized-input lineage and symbolized stack;
- primitive evidence matrix with unknown fields visible;
- approval inbox and immutable audit timeline;
- finding/fix/regression/branch/novelty checklist;
- private report export with sensitivity and disclosure state.

Chat remains useful as an analyst interface attached to a campaign. It should
query durable objects by ID and propose next experiments, not act as the record.

## Implementation sequence

### Phase 0 — make current capability truthful

Deliverables:

- remove or stop registering placeholder `shell`/`http` tools;
- correct README and ADR phase/status drift;
- make one Go toolchain version authoritative across `go.mod`, Dockerfile, CI,
  and contributor docs;
- add `/v1/system` capability flags for execution backend, isolation assurance,
  image pin, RAG, verifier, persistence, and research-mode state;
- document that the present runner requires external PHP apparatus assets and
  does not perform coverage-guided fuzzing;
- add a threat model for the unauthenticated API and runtime workspace mutation.

Exit criteria: the UI/model/operator see only capabilities that can actually
execute, and the documentation makes no campaign-readiness claim.

### Phase 1 — durable research domain and policy

Deliverables:

- versioned domain types, repository interfaces, migrations, SQLite metadata,
  private content-addressed artifact storage, and append-only audit events;
- `AuthorizationScope` validation, expiry, target-root/repository allowlists,
  actor identity, roles, and approval decisions;
- authentication for all non-health endpoints when research mode is enabled;
- fixed operator-configured workspace roots; runtime selection may choose only
  descendants, never an arbitrary host path;
- campaign state machine with idempotent transitions and recovery after restart.

Exit criteria: campaigns and approvals survive restart; an unauthenticated,
expired, out-of-scope, or unapproved action cannot enqueue a worker.

### Phase 2 — runner v2 and artifact custody

Deliverables:

- `Runner`/`Job` abstraction and worker lifecycle separate from `tools.Tool`;
- rootless-Docker preflight and gVisor backend spike; mandatory image digest;
- per-run source/build/corpus/output mounts and a hardened artifact collector;
- typed results, resource accounting, restart cleanup, cancellation, queue and
  global/per-campaign concurrency budgets;
- live containment tests for network, credentials, mount writes, symlink/device
  export, process orphans, disk/inode exhaustion, and cross-campaign isolation.

Exit criteria: a synthetic hostile worker cannot escape the declared boundary,
and every accepted artifact has content ID, origin, type, size, and lineage.

### Phase 3 — first real native-code apparatus

Deliverables:

- manifest and adapter SDK;
- pinned Clang/toolchain builder image and ASan/UBSan variants;
- real libFuzzer target build, seed corpus, dictionary/options support, crash
  artifact prefix, corpus merge/minimize, coverage and offline symbolization;
- one deliberately vulnerable micro-fixture and one real known-bug target;
- build and runtime provenance attached to every result.

Exit criteria: both fixtures are found from a clean campaign, a non-trigger
control stays clean, and the known bug is never labelled novel.

Implementation status: the adapter SDK, pinned native Clang apparatus,
micro-fixture, and the real-program libpng known-bug apparatus are implemented.
The libpng calibration uses an independent seed and keeps its public reproducer
as explicitly known benchmark evidence. Exact vulnerable/fixed builds and the
positive/negative replay controls pass through the real Docker broker. A
repeated clean-corpus discovery/minimization run through the complete campaign
state machine is still required to close this phase's exit criterion.

### Phase 4 — ingestion, triage, and primitive evidence

Deliverables:

- bounded parsers with golden fixtures for ASan, UBSan, MSan and common signals;
- canonical stack normalization, crash signatures and root-cause deduplication;
- deterministic replay policy, minimization identity check, symbolization, and
  source-location linkage;
- primitive evidence matrix and monotonic label gate;
- read-only research query tools that return bounded summaries by object ID.

Exit criteria: duplicated inputs collapse into the correct root-cause group;
timeouts/OOM/assertions/benign UBSan observations do not become memory-safety
findings; a primitive label cannot exist without its required evidence.

Implementation status: the bounded parsers, replay/minimization/symbolization
gates, crash grouping, primitive matrix, monotonic finding labels, and an
authenticated campaign-scoped `research_query` model tool are implemented and
fixture-tested. The query tool exposes metadata and opaque IDs only, filters
campaign membership, removes artifact storage paths, and caps every response.

### Phase 5 — branch, novelty, fix, and regression workflow

Deliverables:

- supported-revision matrix and comparison runner;
- dedicated NVD/vendor advisory/upstream-history/issues/changelog/regression-
  corpus lookup clients with captured source metadata;
- fix worktree generation behind approval, diff review, rebuild, original
  reproducer, regression suite, and negative-control execution;
- private finding/report template and human disclosure checkpoint.

Exit criteria: “novelty unverified” is the default; absence from NVD cannot pass
the novelty gate; every proposed fix has runtime validation and retained logs.

Implementation status: the durable gates, authenticated APIs/UI, fixed HTTPS
lookup broker, approved patch custody, isolated patched-build path, typed
reproducer/regression/negative-control runs, private report renderer, and human
disclosure checkpoint are implemented and fixture-tested. Comparisons against
the primary or separately captured campaign-owned supported revisions are
machine-ingested and bound to exact target/build provenance; genuinely
unavailable revisions can carry an explicit reviewer-owned `untested` reason.
Automatic upstream supported-version discovery and a pinned real-program
campaign remain open release work.

### Phase 6 — research UI and remote workers

Deliverables:

- campaign-centric web views and approval UI;
- resumable event API shared by web and a future terminal client;
- dedicated remote worker protocol using short-lived workload identity, signed
  job envelopes, artifact hash verification, and no inbound control-plane secret;
- optional ClusterFuzzLite/OSS-Fuzz-compatible export for continuous campaigns.

Exit criteria: a campaign can be operated and audited without reconstructing
state from chat, and a disconnected client can resume without losing run state.

## Evaluation strategy

Use three layers; do not judge success from anecdotes.

### 1. Component correctness

- parser golden tests and mutation/fuzz tests for untrusted result ingestion;
- migration, transaction, idempotency, crash-recovery, and content-hash tests;
- policy matrix tests for actor/scope/operation/path/network/expiry/approval;
- state-transition property tests proving labels cannot skip evidence gates;
- runner unit tests plus opt-in live isolation tests on each supported backend.

### 2. Known-ground-truth campaigns

- micro-fixtures for every supported sanitizer/observation class, with clean
  twins to measure false positives;
- NIST SARD/Juliet subsets for source-analysis and claim-classification tests;
- Magma or equivalent real-program ground-truth bugs for end-to-end discovery;
- pinned OSS-Fuzz-style project integrations to validate build/harness/corpus
  portability and reproduction semantics;
- the repository's PHP scanf case study as a regression/claim-calibration case,
  not as proof of general fuzzing capability.

### 3. Product metrics

- target and harness build success rate;
- harness executions/second and coverage growth over time;
- known-bug recall and time to first crash/reproduction/minimization/root cause;
- unique root-cause precision after deduplication;
- crash-to-actionable-finding conversion rate by observation class;
- false promotion rate for timeout/OOM/assertion/benign UBSan/known-issue cases;
- evidence completeness and unsupported-claim rate in final reports;
- fix compile/regression success and recurrence rate;
- artifact-integrity failures, cross-campaign leakage, policy bypasses, orphaned
  workers, and containment violations (all must be zero in release gates);
- model/provider comparison on the same fixed apparatus and budgets, keeping
  runtime evidence constant so model prose is not confused with discovery.

Fuzzing is stochastic. Compare engines/planners with repeated trials and report
distributions, not a single winning run. FuzzBench is the appropriate reference
shape when engine performance itself is evaluated.

## Release gates

Research mode is beta-ready only when all of these are true:

1. Authentication, scope, approval, and audit controls are enforced outside the
   model and tested end to end.
2. No host shell or arbitrary model-controlled network primitive exists.
3. The worker backend verifies its isolation assurance and exact image digest or
   refuses to run.
4. A real coverage-guided engine produces persistent corpora/crashes/coverage.
5. Artifacts are hashed, typed, lineage-tracked, size-bounded, and private.
6. Campaigns resume after control-plane restart without duplicating work.
7. Evidence states and primitive labels are deterministic and monotonic.
8. Known-bug benchmarks meet declared discovery/reproduction/minimization goals
   while clean controls and non-security observations are not promoted.
9. Supported-branch and novelty gates default to unverified, never to affected or
   novel, on missing evidence or failed egress.
10. Fixes and regression tests are executed in the same evidence pipeline.
11. External disclosure is a human-only action.
12. Security review covers control plane, worker runtime, artifact ingestion,
    prompt injection, supply chain, multi-campaign isolation, and secret handling.

## Immediate backlog (first implementation slice)

Implement in this order:

1. Phase-0 truth fixes and capability reporting.
2. `internal/research/domain` with campaign/scope/run/artifact/crash/finding
   types and explicit state transitions.
3. Repository interfaces plus a SQLite/artifact-store spike and migrations.
4. Research-mode authentication and a fixed workspace-root policy.
5. `internal/research/runner` interface, typed result, job journal, and fake
   runner for deterministic API/state-machine tests.
6. Rootless Docker preflight plus per-run output/corpus volumes and artifact
   collector; keep the current contained runner as a compatibility adapter only.
7. One libFuzzer micro-fixture vertical slice through build -> fuzz -> ingest ->
   reproduce -> minimize -> symbolize -> group -> report.
8. Add a second known real-world bug and clean negative controls before adding
   autonomous planning or more target languages.

Do not begin with a larger model, LangGraph, arbitrary shell, a custom fuzzer,
or a polished dashboard. The evidence pipeline and containment contract are the
product; model and UI sophistication only become valuable after those are real.

## Primary implementation references

- LLVM libFuzzer: https://llvm.org/docs/LibFuzzer.html
- Clang AddressSanitizer: https://clang.llvm.org/docs/AddressSanitizer.html
- Clang UndefinedBehaviorSanitizer:
  https://clang.llvm.org/docs/UndefinedBehaviorSanitizer.html
- OSS-Fuzz new-project integration:
  https://google.github.io/oss-fuzz/getting-started/new-project-guide/
- OSS-Fuzz reproduction:
  https://google.github.io/oss-fuzz/advanced-topics/reproducing/
- OSS-Fuzz Fuzz Introspector:
  https://google.github.io/oss-fuzz/advanced-topics/fuzz-introspector/
- ClusterFuzzLite: https://google.github.io/clusterfuzzlite/
- Docker rootless mode: https://docs.docker.com/engine/security/rootless/
- Docker seccomp profiles: https://docs.docker.com/engine/security/seccomp/
- gVisor security architecture: https://gvisor.dev/docs/architecture_guide/intro/
- SLSA provenance: https://slsa.dev/spec/v1.2/provenance
- SARIF 2.1.0: https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/
- NIST SARD/Juliet:
  https://samate.nist.gov/SARD/test-suites/112National
- Magma ground-truth fuzzing benchmark: https://github.com/HexHive/magma
- FuzzBench: https://google.github.io/fuzzbench/
