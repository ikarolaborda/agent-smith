# Research release-evidence gates

- Status: mandatory before any beta claim
- Product posture: `research_beta_ready` is deliberately `false`
- Evidence rule: a pass is retained evidence, not an operator assertion

This runbook separates controls implemented in the repository from evidence
that only a real deployment, benchmark campaign, or independent reviewer can
produce. The `/v1/system/capabilities` response lists the open blocker IDs so a
UI, model, deployment pipeline, or release process cannot infer readiness from
the presence of research endpoints.

## Required dossier

Create one private release dossier for the exact application commit,
apparatus-catalog key ID, source-manifest key ID, image digests, host kernel,
container runtime, filesystem/mount configuration, and configuration hashes.
Every item below needs an owner, execution time, retained log/report SHA-256,
reviewer, and expiry or rerun condition. Keep embargoed bytes in artifact
custody and use only opaque IDs in the dossier.

| Blocker ID | Owner | Pass evidence |
| --- | --- | --- |
| `production_backend_live_qualification` | Runtime/security engineering | Rootless Docker or the selected stronger runtime passes preflight and all opt-in containment tests on every supported production host/backend. Retain exact image/runtime/kernel identities and complete test logs. Rootful lab results do not pass. |
| `kernel_storage_quota` | Storage/platform engineering | Every writable staging and corpus tree has kernel/filesystem hard byte and inode limits at or below the authorized job budget before target code starts. Hostile writes must fail at the kernel boundary without relying on the polling monitor. Retain mount/quota configuration, before/after quota queries, and overrun test output. |
| `complete_cpu_rss_accounting` | Runtime engineering | Cgroup CPU and peak-memory values are captured into `ResourceUsage`, reconcile within the declared measurement tolerance, and over-budget workloads are killed. The existing CFS/memory limits remain enforcement but are not complete accounting evidence. |
| `repeated_clean_corpus_real_program_discovery` | Research evaluation | A preregistered repeated-trial policy (at least three clean starts per configuration) discovers, reproduces, minimizes, symbolizes, and groups the pinned real known bug through the complete campaign pipeline. Retain every success and failure; the public reproducer cannot seed discovery. Clean/non-trigger controls remain unpromoted. |
| `real_program_branch_novelty_remediation_validation` | Research evaluation plus reviewer | The same pinned real-program campaign records supported-revision results, all required fixed-source novelty captures/reviews, an approved candidate diff, patched build, original reproducer, regression run, negative control, and private report. Missing/failed evidence must remain unverified. |
| `deployment_backup_restore_and_destruction_exercise` | Storage/security operations | Restore a storage-consistent encrypted backup into an isolated directory, run `--verify-research-store` with the complete historical keyring, and retain its passing JSON report. Separately prove recovery-point objectives, backup-media expiry after approved purge, snapshot/replica policy, and the documented limits of physical erasure. |
| `signing_and_custody_key_governance_exercise` | Security operations | Exercise staged public-key overlap, new-key signing, observed key-ID change, old-key removal/revocation, artifact-key rotation, restore with escrowed historical keys, emergency revocation, access review, and lost-key response. Private signing keys remain offline and separate from the research host. |
| `trusted_scanner_builder_registry_and_dependency_mirrors` | Supply-chain security | Approve scanner/builder identities, curated transparent registry policy, exact image signature/provenance verification process, and available digest-pinned dependency mirrors. Retain reviewed SPDX/SLSA evidence and catalog-signing records. |
| `independent_security_review` | Reviewer independent of implementation | Review authentication/authorization, approval separation, audit rollback risk, container/kernel escape surface, project-quota provisioning, hostile ingestion, cryptography/key handling, supply chain, egress/SSRF/DNS rebinding, prompt injection, multi-campaign isolation, backup/destruction, and secret handling. All critical/high findings are fixed and retested; accepted lower risks have named owners and dates. |

## Repository gates before the dossier

Run these on the exact release commit and retain the output:

```sh
go test ./...
go test -race ./internal/research/... ./internal/server ./cmd/agent
go vet ./...
npm --prefix web run build
GOOS=windows GOARCH=amd64 go test -exec=true ./internal/research/...
GOOS=darwin GOARCH=arm64 go test -exec=true ./internal/research/...
```

Run the live containment and real-program suites only on the production-like
qualification host using the documented opt-in environment variables. Do not
convert a skipped live test into a pass.

## Kernel quota boundary

The control plane intentionally does not acquire quota-administration
privilege. Linux quota limit changes require privileged administration, so the
platform must provision and verify project quotas outside the application.
Project inheritance must cover newly created files in both staging and corpus
trees, and quota IDs must not be reused while prior files remain. The current
userspace byte/inode monitor stays enabled as defense in depth but cannot close
this gate because a worker can overshoot between polls.

Useful primary references:

- [Linux `quotactl(2)`](https://man7.org/linux/man-pages/man2/quotactl.2.html)
- [Linux kernel ext4 project-quota format](https://www.kernel.org/doc/html/latest/filesystems/ext4/globals.html)
- [XFS project quota inheritance](https://www.kernel.org/pub/linux/utils/fs/xfs/docs/xfs_filesystem_structure.pdf)

## Release decision

No partial score passes. Any missing, expired, skipped, unverifiable, or
configuration-mismatched item leaves the product pre-beta. A future readiness
switch must verify a signed, short-lived dossier and the live runtime posture;
it must not be a boolean command-line override.
