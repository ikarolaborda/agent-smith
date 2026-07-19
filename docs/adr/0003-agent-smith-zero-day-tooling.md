# ADR 0003 — Making agent-smith a powerful AI zero-day research tool

- Status: Proposed (plan; implementation phased, see Roadmap)
- Date: 2026-06-17
- Context: a Tier-1 capability assessment (ikarolaborda/php74-vuln-research
  `CAPABILITY_ASSESSMENT.md`) established that agent-smith is today a strong,
  honesty-enforcing vulnerability-research **copilot** but **not** an autonomous
  0-day finder. The binding constraint is the **execution/feedback tool surface**
  — not the model (configured as GPT5.5-cyber). Its working tools are
  `file_read`/`read_dir`/`file_edit`/`file_write` (workspace-scoped) + `http`; the
  `shell` tool is a stub.

## Decision

Build agent-smith into a 0-day **researcher loop** by adding a *safely contained*
execution primitive and a result-ingestion path, reusing the anti-fabrication
guardrails already shipped. **We will NOT ship a host-level `sh -c` "shell" tool**:
a host exec with only `cwd`+timeout+output-cap is **not a sandbox** (see Threat
model) and shipping it as one would be the exact false-safety overclaim this
project exists to prevent (buddy review `01KVAFS7…` rejected it). The execution
primitive must be **OS/container-contained** from v1.

## Target architecture — the researcher loop

```
 (LLM: GPT5.5-cyber)                      (NEW: contained execution)
 audit + hypothesize  ──►  exec_tool: build / fuzz / reproduce in a sandbox
        ▲                              │
        │                              ▼
   triage + NOVELTY GATES  ◄──  ingest: sanitizer logs / crashing inputs / coverage
   (already built:                     │
    NVD verifier, cross-provider       ▼
    validation, grounding directive,  responsible disclosure (private)
    methodology corpus)
```

The model proposes; the **contained tool executes**; the **evidence feeds back**;
the **guardrails keep every claim at the tier it earned** (crash-only findings).

## Threat model (why host `sh -c` is rejected)

Giving an LLM arbitrary execution is the **highest-risk tool in the system**
(prompt-injection, tool misuse, accidental destructive commands). `cwd=workspace`
does NOT confine the filesystem (absolute paths, `..`, symlinks, redirection,
mounted secrets, SSH/Git/cloud creds, inherited env). Timeouts/output caps do not
stop backgrounding (`nohup`/`&`/`setsid`), fork bombs, disk fill, network
exfiltration, or persistence. Therefore the execution tool MUST specify and enforce:

- **Containment boundary:** ephemeral OCI container (operator already runs the
  apparatus in Docker) or an OS sandbox (nsjail/bwrap/gVisor). Read-only base
  rootfs; only the workspace + a tmp dir are writable mounts.
- **Credentials:** synthetic `HOME`; strip SSH/Git/cloud/browser tokens; fixed
  minimal `PATH`; explicit env allowlist only.
- **Network:** disabled by default; allowlisted egress only when a task needs it
  (e.g. cloning php-src) and never reaching metadata/internal services.
- **Process control:** own process group; recursive child kill on timeout/cancel;
  max runtime, max stdout/stderr, max file size / disk quota, concurrency cap.
- **Auditability:** disabled by default; explicit operator opt-in; startup banner
  that exec is enabled; per-command log of command + exit code (redacted).

A structured **build/fuzz runner** (fixed subcommands + typed args for the
sanitizer/fuzzer apparatus) is preferred over arbitrary shell; if a general shell
is offered, it is **break-glass**, container-confined, not the default path.

## Roadmap (phased; each phase independently shippable + tested)

1. **Contained exec primitive (next session).** A `run` tool that executes a fixed
   set of apparatus operations (build / fuzz / reproduce / triage) **inside an
   ephemeral container** with the containment policy above. Disabled by default;
   `--allow-exec` (CLI) / `Options.AllowExec` (server). Wiring:
   `NewDefaultRegistry(workspace)` stays unchanged (delegates `allowExec=false`);
   add `NewDefaultRegistryWithExec(workspace, allowExec)`. Tests prove:
   disabled-by-default, network-off, no-write-outside-mount, process-tree kill on
   timeout, output truncation, env scrubbing.
2. **Structured result ingestion.** Parse sanitizer logs / crashing inputs /
   coverage into typed results fed back into the agent context so it can minimize
   + root-cause autonomously.
3. **Novelty + disclosure automation.** Wire the existing NVD verifier + the
   novelty gates (git log / NEWS / supported-branch) into the loop so a crash is
   auto-classified known vs candidate before any claim; private-disclosure draft.
4. **External fuzzing service over `http`** (no local exec): orchestrate a CI/cloud
   fuzzer via the existing `http` tool — the lowest-risk autonomy path.

## Consequences

- agent-smith moves from copilot toward **semi-autonomous**: it can drive the
  apparatus and reach Tier-1/2 evidence, while the guardrails keep it from
  fabricating. The model (GPT5.5-cyber) improves hypothesis/triage quality; it is
  the *tools* that raise the evidence tier.
- The execution tool is **off by default**; enabling it is an explicit, audited,
  operator decision — the safety boundary is containment + opt-in, not trust alone.
- Findings remain **crash-only**; "0-day" is reserved for a reproduced,
  minimized, novelty-gated, supported-branch-relevant crash.

## Phase-1 acceptance criteria (the next session MUST prove all)
Host `sh -c` / cwd+timeout+rlimit execution is **prohibited** and is not an
acceptable fallback. Execution runs ONLY inside an ephemeral OCI container or
equivalent OS sandbox (nsjail/bwrap/gVisor — these are NOT interchangeable; pick
one and document its capability), with tests that prove each invariant:
- network is OFF by default (deny egress; prove a network call fails),
- cannot write outside the workspace+tmp mounts (prove a write to `/` / `$HOME` fails),
- rootfs is immutable except allowed mounts,
- credentials are stripped (no SSH/Git/cloud tokens reachable; synthetic HOME),
- the process tree is killed on timeout/cancel (no orphans),
- stdout/stderr/disk/CPU/memory/runtime are bounded and truncation is reported,
- the tool is DISABLED by default; enabling requires `--allow-exec` + an audit log line per command.

## Non-goals (phase 1)
No privilege-escalation testing, no persistence, no credential access, no
unrestricted outbound network, no host-namespace sharing, no automatic
disclosure/publication. The break-glass general shell (if offered at all) is
exceptional, audited, and runs inside the SAME containment layer — it must not
grow into the default path. The external fuzzing service (phase 4) is explicitly
out of scope for phase 1.

## Result-ingestion schema (define now, implement in phase 2)
Treat all tool output as UNTRUSTED, typed, and size-limited:
`{ exit_status, signal_or_timeout_reason, stdout_truncated, stderr_truncated,
   bytes_dropped, artifact_paths[], resource_usage{cpu,mem,wall,disk},
   audit_correlation_id }`.

## Phase-1 status (IMPLEMENTED)
Shipped as the `run` tool (`internal/tools/builtin/exec.go`): a structured runner
with operations `fuzz` / `reproduce` / `triage` (no arbitrary shell); `build` is
intentionally refused (it needs network + host image construction — run
`scripts/build.sh` on the host). Each run is an ephemeral `docker run --rm` with
`--network=none`, `--read-only` rootfs, `--cap-drop=ALL`,
`--security-opt=no-new-privileges`, `--user=65534:65534`, a tmpfs `/tmp` as the
only writable path, the workspace mounted **read-only** at `/work` (the container
only reads corpus/harnesses; output is captured host-side), bounded
memory/pids/cpus, and an explicit container env allowlist (no host env is ever
forwarded). Disabled by default; enabling requires `--allow-exec` (CLI) /
`Options.AllowExec` (server) plus a workspace, emits a startup banner, and logs a
redacted audit line per command. On timeout the host process group is SIGKILLed
and `docker rm -f <name>` reaps the container (killing the CLI alone does not).
Containment invariants are proven by Tier-1 tests over the pure argv builder and
the OS process-group kill, and were demonstrated live (Tier-2) against the
`php74-asan` image: writes to `/etc` and `/work` fail, `/tmp` is writable, and no
usable NIC exists under `--network=none`.

## Residual trust (phase 1 — NOT a VM-grade sandbox)
Containment reduces blast radius; it does not eliminate it. The host trusts: the
local Docker daemon and the kernel/container isolation boundary (a daemon or
kernel compromise defeats the flags); the validation **image** (a malicious or
drifted image can taint results or exploit a runtime bug); and the robustness of
host-side consumers of the captured, size-bounded, untrusted output. Phase-2+
hardening: `--pull=never` is now applied (the daemon resolves only the local
image and fails closed — no run-time registry pull of the `php74-asan` tag, so
cache-poisoning / typosquat image substitution cannot bypass `--network=none`,
which confines only the container). This intentionally trades convenience for
fail-closed local-image trust: the run tool translates the daemon's
image-absent error into an actionable "build it on the host first
(scripts/build.sh)" hint rather than surfacing a raw Docker fault. The
local-re-tag residual is now closeable: an operator MAY pin the apparatus image
to an exact local image ID via `--exec-image-digest sha256:<hex>` (the
`WithExpectedImageDigest` option). Because a locally-built image has no registry
RepoDigest, the pin is the bare image ID — which `docker run` accepts as a
reference — not `name@sha256`; combined with `--pull=never` this resolves the
exact image content or fails closed, so a re-tag of the `php74-asan` tag to
different content is no longer honored. The pin is opt-in (unset = resolve by
tag) because the digest is host-specific. Still deferred: a tighter seccomp
profile and image provenance/signature verification.

## Capability correction (2026-07-19)

Phase 1 proves a useful containment boundary, but it is not a complete fuzzing
apparatus. This repository does not contain the referenced `scripts/build.sh`,
the `php74-asan` apparatus Dockerfile, PHP harnesses, or seed corpora. The
current `fuzz` operation invokes a PHP driver over a corpus path; it does not
link or run a coverage-guided engine, evolve a writable corpus, or persist crash
artifacts. The workspace is intentionally mounted read-only and `/tmp` is
ephemeral. Treat the tool as a compatibility adapter for an externally prepared
lab until the runner-v2, artifact-ingestion, and libFuzzer phases in
`docs/plans/cybersecurity-research-platform.md` are implemented.

The non-functional `shell` and `http` placeholders are no longer registered as
model tools. Networked acquisition and novelty checks must use dedicated,
bounded clients; target execution remains structured and container-contained.

## Rejected alternatives
- **Host `sh -c` with cwd/timeout/cap "sandbox"** — rejected (not a sandbox;
  credential/exfil/escape risk).
- **Bigger/cyber model alone** — does not raise the evidence tier without execution.
