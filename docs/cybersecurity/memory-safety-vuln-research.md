# Grounded Memory-Safety Vulnerability Research (and the Novelty Trap)

This document teaches the agent how to do **research-grade, evidence-grounded**
memory-safety vulnerability discovery on native code (C/C++ such as `php-src`,
image/parser libraries, runtimes) — and, more importantly, how **not** to
overclaim. It exists because the single worst failure mode for a security agent
is a **confident fabricated finding**: a "0-day" or CVE that does not survive
verification. Every rule here is built so that what the agent reports is only
ever what a verifier actually earned.

The operating sentence: **route, then verify, then reason, then assert only at
the confidence the route earned.**

## 0. What counts as a finding

A **finding** is a *reproduced sanitizer crash or memory-corruption report with a
saved, minimized input and a symbolized stack trace*. Nothing else is a finding:

- A source read that "looks suspicious" is a **hypothesis**, not a finding.
- A behavioral difference between versions is a **hypothesis**, not a finding.
- A model's belief that a function is vulnerable is a **hypothesis**, not a finding.
- A sanitizer report that is not attacker-controlled and not memory-corrupting is
  an **observation**, not a vulnerability.

Use the evidence tiers: Tier-1 (sanitizer/compiler/test), Tier-2 (reproduction in
a sandbox), Tier-3 (primary source / advisory). Tier-4/5 (blogs, pure reasoning)
may *propose* but never *confirm*.

## 1. The research apparatus

1. **Sanitizer build (Tier-1 foundation).** Build the target from source with
   AddressSanitizer + UndefinedBehaviorSanitizer and debug info, pinned to the
   architecture real deployments use (e.g. `linux/amd64`), not the host arch.
   Example flags: `-O1 -g -fno-omit-frame-pointer -fsanitize=address,undefined`.
2. **Reuse the project's own fuzz harnesses.** Mature C projects ship them
   (e.g. `php-src/sapi/fuzzer/` provides `fuzzer-unserialize`, `fuzzer-parser`,
   `fuzzer-json`, `fuzzer-exif`). Build with `--enable-fuzzer` and feed them with
   libFuzzer. Reusing real harnesses beats hand-rolling.
3. **Seed corpus from the project's own tests.** The regression tests
   (`ext/<x>/tests/`) are high-quality seeds that already exercise the parser.
4. **Attack-surface ranking.** Hunt parsers/decoders that consume
   attacker-controlled bytes with manual memory handling first (image/metadata
   decoders, archive formats, deserializers), and rank by: memory-unsafe code
   density, parser complexity, historical bug volume, harnessability, input
   controllability. Defer narrative-heavy classes (PHP-level POP chains) — the
   memory-safety angle is the native low-level parsing, not the object graph.
5. **Triage:** symbolize, bucket crashes by top frames, **re-run identically** to
   confirm determinism, discard flaky/timeout-only cases, then **minimize** the
   input to the smallest trigger and identify the offending line + violated
   invariant.

## 2. THE NOVELTY TRAP (the core safeguard)

**A reproduced crash is necessary but NOT sufficient to call something a 0-day.**
On end-of-life software (e.g. PHP 7.4, EOL 2022-11-28) a sanitizer build will
mostly rediscover bugs that are **already known, already fixed upstream, or
already CVE'd**. Treating any crash as a "0-day" is the fabrication trap in a new
costume.

Before *any* novelty label, ALL of these gates must pass:

1. **Reproducibility gate** — deterministic repro with a saved minimized input,
   exact build commit, configure flags, and sanitizer output.
2. **Security-relevance gate** — is it actually memory corruption with attacker
   influence? Assertions, leaks, OOMs, hangs, integer-overflow-without-effect,
   and benign UBSan pointer-arithmetic reports are usually **not** security bugs.
   Use conservative wording: "candidate memory-safety issue" until impact is shown.
3. **Attacker-control gate** — is the crashing input something an attacker can
   actually supply at a trust boundary? A crash that only fires from local config
   or program startup is not remotely interesting.
4. **Novelty gate (beyond NVD)** — absence from NVD is **insufficient**. Also
   search: `php-src` git log + `git blame`/history for the touched function,
   `NEWS`/`ChangeLog`, the bug tracker/issues, commit messages mentioning
   crashes/asserts/CVE fixes, and existing fuzzer regression corpora that may
   already cover the root cause.
5. **Supported-branch gate** — does it reproduce on a *currently supported*
   branch (8.x)? If it is fixed there, it is a **known/EOL-only historical bug**,
   not a current 0-day. A real current 0-day must affect supported code or carry
   clear evidence of supported-version impact.
6. **Duplicate-root-cause gate** — many crashing inputs on one code path are
   **one** candidate until root-cause analysis distinguishes them. Do not inflate
   counts.

Only a crash that passes every gate is a **candidate 0-day** — and even then it
is "candidate", handled via **private responsible disclosure**, never announced.

### Output labels (use these exact words, in increasing strength)
`crash observed` → `candidate memory-safety issue` → `novelty unverified` →
`known issue likely` → `supported-branch status: <untested|fixed|affected>` →
`candidate 0-day (disclosure pending)`. Never write "0-day" unless every gate passes.

## 3. Anti-fabrication rules (hard)

- Never infer **exploitability** from a crash alone.
- Never infer **novelty** from absence in a quick search.
- Never claim **CVE-worthiness** without upstream validation.
- Never present a **background/in-progress** fuzz run as completed analysis.
- Never assert a CVE id / CVSS / affected-version range that is not in retrieved
  context (this is what the NVD verification gate and the cross-provider
  validation layer exist to catch — let them).
- A blunt "crash observed; novelty unverified; likely a known mbstring nit" beats
  a smooth "we found a 0-day" every time.

## 4. Sanitizer-report interpretation

Sanitizer output can be compiler-, config-, or harness-induced. Before trusting it:
re-run with the same input, get a **symbolized** trace, and where possible confirm
under a second configuration or with a reduced reproducer. UBSan reports in
particular (e.g. "applying non-zero offset to null pointer") are frequently
**benign** undefined behavior that never corrupts memory.

### Worked example — OBS-0001 (a sanitizer report that is NOT a vulnerability)
Running a freshly ASan+UBSan-built PHP 7.4.33 (`php -v`, commit `004cb8275…`,
clang 11) emits, on startup:

```
ext/mbstring/mbstring.c:784: runtime error: applying non-zero offset 1 to null pointer
  #0 php_mb_parse_encoding_list  ext/mbstring/mbstring.c:784
  #1 _php_mb_ini_mbstring_http_input_set  ext/mbstring/mbstring.c:1280
  #2 OnUpdate_mbstring_http_input  ext/mbstring/mbstring.c:1301
```

Apply the gates: it fires on **startup with no input** (fails the attacker-control
gate), it is a **UBSan null-pointer-offset** with no memory corruption (fails the
security-relevance gate), and it is in a **well-known benign-UB class** that newer
PHP cleaned up (fails the novelty gate). **Verdict: observation, not a
vulnerability.** The correct action is to record it as an observation and, for the
apparatus, drop the offending extension (mbstring is not needed to fuzz
unserialize/exif/json) or add a UBSan suppression so the binary stops aborting on
a harmless startup nit — then continue the real hunt. *This is the discipline
working: the stack found something real on its first run, and the disciplined
response was to NOT call it a 0-day.*

## 5. Responsible disclosure

If a candidate survives every gate and is plausibly security-relevant on a
supported branch: capture the full artifact set (minimized PoC, symbolized trace,
affected commit/tag, build recipe, branches tested, novelty notes), report
privately to the upstream security contact (e.g. `security@php.net` and/or the
GitHub Security Advisory flow), and keep public notes high-level until maintainers
respond. Do not publish weaponized detail. EOL status does not remove the duty to
disclose responsibly, because the same code path often persists in supported branches.

## 6. Candidate handoff template (keep every campaign honest + transferable)

```
Candidate: <H-id>
target/harness: <fuzzer-unserialize | exif | ...>   build commit: <hash>   flags: <...>
reproducer: <path>   minimized: <path>   deterministic: <yes/no>
sanitizer output: <symbolized trace>
bug class: <heap OOB read | UAF | ...>   security relevance: <argued, conservative>
attacker control: <trust boundary / input vector>
novelty checks: <NVD | git log | NEWS | issues | fuzzer corpora — results>
supported-branch status: <untested | fixed-in-8.x (=> known) | affected>
label: <crash observed | candidate issue | candidate 0-day (disclosure pending)>
disclosure status: <none | private report sent>
```

The honest default for any fresh campaign is: **"No confirmed new vulnerability —
apparatus validated, observations triaged, novelty unverified."**
