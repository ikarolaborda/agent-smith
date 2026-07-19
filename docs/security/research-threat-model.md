# Research-mode threat model

- Status: implemented baseline, security review required before beta
- Last reviewed: 2026-07-19
- Scope: authorized source-code research and minimal exploit-primitive characterization

## Assets and invariants

The protected assets are authorization scopes, embargoed source and findings,
provider credentials, campaign metadata, crashing inputs, corpora, instrumented
binaries, symbols, reports, audit identity, and the integrity of finding labels.
The following invariants are enforced outside model output:

1. An absent, expired, revoked, mismatched, or over-budget scope cannot enqueue a
   worker.
2. Research API mutations have an authenticated principal; `/healthz` and static
   login assets contain no research data and are the only anonymous routes.
3. Runtime workspaces remain descendants of fixed operator-configured roots,
   including after symlink resolution.
4. Jobs match a registered, scope-allowlisted apparatus manifest and exact image
   SHA-256 identity. The runner does not invoke a host shell.
5. Worker network is disabled, credentials are absent, the root filesystem is
   read-only, capabilities are dropped, privilege escalation is disabled, and
   memory/PID/wall limits are set. Runner v2 refuses startup unless Docker reports
   rootless mode, built-in seccomp, and usable cgroups (cgroup v2 for rootless);
   an optional runtime such as `runsc` must exist when configured.
6. Worker output is hostile. Only regular files matching typed role/count/size
   rules are ingested; symlinks, devices, unexpected files, path escapes, changed
   files, excess logs, and hash mismatches are rejected.
7. Artifact bytes are SHA-256 addressed, private on disk, campaign-bound, and
   rehashed before download. Metadata mutations and state transitions are
   version checked; audit events are append-only and hash chained.
8. Evidence states and finding labels are monotonic. Timeouts, OOMs, assertions,
   leaks, bare signals, and UBSan observations are not silently promoted to
   memory-safety findings. Missing novelty data remains `novelty_unverified`.
9. External disclosure is not implemented as an automated side effect. The
   application can render only a private evidence-linked draft after review.

## Trust boundaries and primary threats

### Browser and HTTP control plane

Threats include stolen bearer tokens, CSRF-like cross-origin calls, oversized
bodies, role confusion, object-ID guessing, cross-campaign reads, and a hostile
prompt asking the model to invoke control-plane actions. Research tokens are
minimum-length secrets held in tab-scoped storage; CORS is not treated as auth;
campaign reads apply scope membership; bodies/lists are bounded; raw evidence
facts cannot be supplied through the API. TLS and external identity federation
remain deployment responsibilities when binding beyond loopback.

### Model and retrieved content

Model text, tool arguments, source comments, web pages, issue bodies, sanitizer
logs, and filenames are untrusted. Models may propose plans and experiments but
cannot promote evidence, register an apparatus, approve their own action, change
fixed roots, choose arbitrary egress, or disclose. Raw logs and binary artifacts
stay outside model context; bounded summaries and opaque IDs are the interface.
The model's `research_query` capability is bound to the authenticated request
principal, applies campaign membership on every lookup, omits artifact storage
paths and bytes, filters private-disclosure metadata by role, and enforces a hard
response-size ceiling.

Novelty egress is disabled without an operator-owned source file. Enabled
lookups use fixed HTTPS origins, refuse redirects and credential headers, apply
response/query limits, retain response hashes and bytes, and still require a
separate human/parser review. Campaign domain policy is enforced before every
lookup; a missing advisory record remains `novelty_unverified`.

### Acquisition and supply chain

Repository/ref resolution, base images, apparatus images, compilers, build
scripts, dependencies, and seed corpora can be malicious or compromised. Scopes
pin permitted repositories/revisions; target records retain source hashes;
apparatus images are exact IDs; the native image build requires a digest-pinned
base and emits compiler/build provenance. Local acquisition rejects links and
special files, applies file/byte ceilings, copies atomically into a private
campaign tree, and rehashes that tree before every worker mount. A production
deployment still needs a curated image registry, signature verification/SBOM
policy, and an explicit acquisition broker before accepting third-party targets.

Candidate fixes are bounded textual unified-diff artifacts tied to an approved
finding correlation. The control-plane host never applies them. A read-only
patch mount is applied to an ephemeral apparatus worktree, and the resulting
build records the patch artifact ID. Remediation cannot advance without clean
original-reproducer, regression-corpus, and distinct negative-control records.

### Worker runtime

Target code may attempt kernel/container escape, fork bombs, disk/inode
exhaustion, secret discovery, network access, mount mutation, process orphaning,
or cross-campaign access. Disposable per-run mounts, rootless Docker/gVisor,
network denial, read-only source/build mounts, per-campaign concurrency, process
cancellation, and strict artifact collection reduce risk. Docker shares a kernel
and is not a perfect sandbox. Docker workers receive a CFS CPU-rate ceiling
derived from the authorized CPU/wall envelope plus memory, PID, file-size,
open-file, and wall-clock limits. The broker continuously measures the writable
output/corpus footprint, rejects links and non-regular entries, records peak
growth, and cancels byte or inode overruns. That monitor is defense in depth,
not a kernel-enforced filesystem quota: disk and inode quotas still depend on
the deployment filesystem/runtime and require live release-gate tests.
Deployments that cannot enforce them must not claim hostile-target isolation.

### Persistence and artifacts

Threats include database rollback/tampering, CAS replacement, malicious media
types, decompression bombs, symlink races, retention mistakes, and unencrypted
embargoed data. Directories use `0700`, files use `0600`, artifact size is capped,
content is rehashed, and storage paths are not exposed by the API. Hash chaining
detects alteration but does not prevent deletion/rollback by a host administrator.
Use an encrypted volume, protected backups, external audit anchors, retention
policy, and OS-level access controls for sensitive campaigns.

### Egress and novelty research

Arbitrary URLs are prohibited. Dedicated lookup clients construct requests from
fixed HTTPS source definitions, cap/cache responses, hash and retain them, and
separate capture from review. DNS/transport policy, vendor-specific allowlists,
rate limits, credentials, and provenance must be reviewed per deployment. An
absence from NVD or any other source never proves novelty.

### Remote workers

Remote job envelopes use short-lived Ed25519 signatures. Result submission uses
one-job/one-worker, one-time workload leases rather than a reusable inbound
control-plane credential, and artifact size/hash verification occurs before
ingestion. Network transport endpoints and workload attestation are not enabled
by default; production remote workers require mutually authenticated transport,
key rotation/revocation, replay storage that survives restart, and isolation
parity with local workers.

## Explicitly out of scope

The platform does not authorize indiscriminate scanning, third-party deployment
targeting, credential access, persistence, evasion, lateral movement, automated
weaponized exploit chains, public proof-of-concept publication, or automatic
external disclosure. Minimal primitive characterization stops at the smallest
effect backed by evidence and records the gap to code execution, disclosure,
privilege change, or cross-boundary impact.

## Beta blockers

Before a beta claim, run live isolation tests on every supported backend;
enforce kernel/filesystem disk and inode quotas; add an acquisition/egress
deployment policy; complete repeated clean-corpus discovery/minimization on the
real known-bug benchmark (the libpng replay calibration alone is not
discovery); validate fix and branch workflows end to end; add encrypted
retention/backup guidance; commission an independent review of authentication,
container escape surface, artifact ingestion, supply chain, prompt injection,
and multi-campaign isolation.
