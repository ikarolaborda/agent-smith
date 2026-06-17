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

## Rejected alternatives
- **Host `sh -c` with cwd/timeout/cap "sandbox"** — rejected (not a sandbox;
  credential/exfil/escape risk).
- **Bigger/cyber model alone** — does not raise the evidence tier without execution.
