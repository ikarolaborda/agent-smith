# PHP-7.4 memory-safety research — campaign case study & reusable method

Distilled, defensive knowledge from a bounded source-first + AddressSanitizer
memory-safety campaign on PHP 7.4.34 (`php-src`, clang ASan/UBSan, amd64). It records
the reusable *method*, the one confirmed finding as a worked example, the recurring
bug class, and the operating boundaries. It teaches how to **find, validate, fix, and
disclose** memory-safety defects — never how to weaponize them.

## TL;DR (what reliably worked)
- **Source-first, then sanitizer.** Build a field→sink table for every
  length/count/offset, prune hypotheses against the real source (Tier-3), then
  corroborate with adversarial ASan replay (Tier-1). On hardened/EOL targets the
  naive memory-safety classes are usually already guarded by accumulated CVE fixes —
  reading the guards is faster than fuzzing blind.
- **`USE_ZEND_ALLOC=0` is mandatory for small-OOB hunts.** PHP's zend_mm rounds
  allocations to size classes, so a `<8`-byte overflow lands in slack and **never
  crashes** under the default allocator. Disabling zend_mm routes `emalloc`→libc
  `malloc`, giving ASan a real redzone. The one confirmed finding in this campaign was
  invisible for 12 prior cycles precisely because this was not set.
- **Calibrated claims.** crash ≠ 0-day; validated-negative ≠ proven-safe; "no public
  report found" ≠ proven-novel; a confirmed OOB write ≠ exploitable. State the
  strongest verifier tier that actually backs each claim and stop there.

## Worked example — the only confirmed finding (CWE-787 off-by-one)
`ext/standard/scanf.c` → `BuildCharSet()` (the `%[...]` scanset parser for
`sscanf`/`fscanf`).

- **Bug:** `cset->chars` is sized `safe_emalloc(sizeof(char), (end - format - 1), 0)`
  (the scanset body length), but the terminal-dash branch (`*ch=='-' && *format==']'`)
  writes **two** chars (`start` + the dash) while the leading-literal block already
  emitted the leading `]`/`-`. For a body that is exactly `<leading-literal><dash>`
  (`[--]`, `[]-]`, `[^--]`, `[^]-]`) total writes = `body_len + 1` → a fixed **+1
  out-of-bounds heap write of size 1**.
- **Trigger:** `sscanf('-', '%[--]')`. Deterministic ASan abort at `scanf.c:198`.
- **Fix (1 line, semantics-preserving):** `(end - format - 1)` → `(end - format)`,
  restoring the function's own "overallocate the set" intent. **Max-write proof:** the
  terminal-dash double-emit is the *only* position whose emit (2) exceeds its body
  consumption (1) and it happens at most once → writes ≤ `body_len + 1`, never `+2`;
  so one extra byte is necessary and sufficient. Runtime-validated: patch + recompile
  → the 4 triggers return normally under `USE_ZEND_ALLOC=0`.
- **Severity: LOW.** Fixed +1 of a predictable byte, into a 2-byte allocation,
  slack-masked in production, reachable only via an attacker-controlled *format*
  string. No exploitation primitive demonstrated. Distinct from CVE-2006-4020 (a `%N$`
  XPG over-read). Present on `master` + `PHP-8.3` (not EOL-only).

## The recurring bug class to look for
**Pre-scan-count then double-emit on a terminal element.** A buffer is sized by
counting input elements in a first pass, but a second pass emits *more than one*
output per element in an edge position (here, the terminal `-` emits `start`+dash). The
allocation under-counts by the number of extra emits. Same family as scanset/charset
builders, run-length expanders, and escape decoders. Diagnostic: for every
`alloc(count_of_X)`, ask "can any single X write more than one element into this
buffer?" and check the first/last-element special cases.

## Validation patterns (defensive, reusable)
- **PoC validation matrix:** trigger cases + a **non-trigger control** (proves the
  harness discriminates, not crashes on anything) + an **apparatus-dependence control**
  (here: drop `USE_ZEND_ALLOC=0` → no crash, proving the slack-masking) + a
  **determinism repeat** (3×). Match crashes by function/path/signature, not a single
  line number (lines shift across builds).
- **Fix validation without a full rebuild:** patch the one TU in the existing ASan
  build tree and run an **incremental `make`** (recompile + relink `sapi/cli/php`,
  minutes) — far cheaper than a from-scratch build, and it gives a real Tier-1
  runtime confirmation that the patch eliminates the crash.
- **Apparatus pitfalls:** giant-`alloc`/giant-count inputs are OOM, not memory bugs —
  exclude them (`allocator_may_return_null=1`). Work factors (crypt `rounds=`, bcrypt
  cost, deep recursion) are DoS/time, not corruption — bound them. Benign UBSan
  "non-zero offset to null pointer" on engine startup/compile/GC is an observation
  class, not a finding.

## Disclosure-readiness gates (for a human to send)
Before reporting to a vendor (`security@php.net`): (1) minimized PoC + symbolized
sanitizer trace + build recipe; (2) **supported-branch confirm** (does it reproduce on
a current release, not just EOL?); (3) **introducing-commit** via `git blame` on a full
clone; (4) **prior-art** sweep (CVE DB, bug tracker, GitHub issues/advisories,
fuzzer corpora) — "not found" is conservative, not proof of novelty; (5) conservative
impact wording. The autonomous agent prepares the draft; a human reviews and sends.

## Operating boundary (hard)
This methodology is for **defensive** memory-safety work: discover → validate →
characterize (including honest exploitability-*gap* analysis) → fix → responsibly
disclose. It explicitly does **not** include building exploitation primitives
(allocator-corruption, heap grooming, control-flow hijack, info-leak) — not on
request, not "via a cluster," not under deadline/reputational pressure, and not by
handing over a step-by-step recipe. Characterizing *why* a bug is hard to weaponize is
legitimate severity-bounding; building the missing primitive is not. A sanitizer-
confirmed minimized reproducer + root cause + fix is a complete, defensible research
deliverable on its own — overclaiming an unproven exploit is what damages a
researcher's credibility; calibrated rigor protects it.

## Honest residuals of this campaign
24 surfaces method-audited, **one** confirmed finding (the scanf off-by-one), seven
benign/DoS observations. Bounded by iteration budget, not exhaustive. Deferred, higher-
cost discriminators: 32-bit/ILP32 runtime (build infeasible under qemu+ASan),
coverage-guided fuzzing (libFuzzer not wired on 7.4), and the introducing-commit /
8.x-patched-build gates. "Near-exhausted" is scoped to the audited no-rebuild amd64
surface — not a proof of absence.

---

## Addendum — exploitation-feasibility & heap-internals lessons (scoped to tested PHP 7.4/8.3, 64-bit, default zend_mm)

These are reusable lessons from severity-bounding the scanf `BuildCharSet` +1 heap
write. They show where model reasoning helps (hypothesis generation, root-cause) and
where only harness validation prevented overclaim.

**Geometric containment is the first test for any fixed small OOB.** A bug reaches a
neighboring allocation only when `write_index >= allocator_min_slot`. Here the write is
at index 2 and zend_mm's smallest slot is 8 bytes (a 2-byte request is already rounded
into the 8-byte class), so the byte stays in the slot's own slack. No grooming moves a
slot boundary. *Verifier-backed:* after a 100-object groom + trigger, the victim object
is intact; this is a runtime fact, not a model assertion.

**"Freelist corruption" on an in-slot overflow is just neighbor-reach relabeled.** To
corrupt a freed slot's `next_free_slot` you must reach a *different* slot — same
boundary, same blocker. *Verifier-backed:* running the exact PoC, the post-trigger
allocation returns a clean value (`val=4242`), not a corrupted pointer. An in-band
freelist link is written at *free* time, so it overwrites any pre-free user byte — "free
then read my byte" never yields attacker data.

**PHP heap-grooming traps the model must not assert past:** `str_repeat($n)` allocates a
`zend_string` (header + payload + NUL → a larger small-bin class, commonly ~32 B), not an
`$n`-byte chunk; `spl_object_id()` returns a recycled object handle, not an address;
under `USE_ZEND_ALLOC=0` an ASan abort is the **redzone detector** firing around a libc
allocation, not evidence the byte reached an adjacent Zend object.

**Process lesson (model behavior):** a single fixed-value 1-byte write is not additive
and not multi-byte; "0x06 + 0x2D", "corrupts bytes 2-3", "lower 6 bytes" are model
confabulations a runtime check kills instantly. And a report that contradicts itself
(LOW in one section, MEDIUM/RCE in another) is discarded in triage — internal
consistency + verifier-backing beats prose confidence. This is the §35 falsification
discipline working as designed: every escalation claim was routed to a runtime verifier
before it was allowed to stand, and each one was defeated.
