# Secure coding & vulnerability classes (defensive)

Knowledge base for finding and FIXING vulnerabilities in owned code. Defensive
and remediation-focused: each class lists the signature, how to detect it, and
the secure pattern. Use it to review code, explain the risk, and produce the
fix + regression test — not to weaponize.

## OWASP Top 10 (2021) — what to look for and how to fix

- **A01 Broken Access Control** — missing authorization checks, IDOR (object id from the request used without ownership check), path traversal, force-browsing. Fix: deny-by-default, server-side authorization on every object access, check ownership/tenant, canonicalize and sandbox file paths.
- **A02 Cryptographic Failures** — secrets in code, weak/absent hashing, plaintext transport. Fix: TLS everywhere; password hashing with bcrypt/argon2id; never roll your own crypto; secrets from env/secret manager, never the repo.
- **A03 Injection** — SQLi, command injection, LDAP/NoSQL injection, XSS. Fix: parameterized queries / prepared statements, allowlist input validation, contextual output encoding, never string-concatenate untrusted input into a query/command/HTML.
- **A04 Insecure Design** — missing threat model, no rate limiting, trust of client-side checks. Fix: threat-model the feature, enforce limits server-side, fail closed.
- **A05 Security Misconfiguration** — verbose errors, default creds, open CORS, debug on in prod. Fix: hardened defaults, least privilege, disable debug, restrict CORS to known origins.
- **A06 Vulnerable/Outdated Components** — known-CVE dependencies. Fix: SBOM + dependency scanning (e.g. `govulncheck` for Go, `composer audit` for PHP), pin and patch, track advisories.
- **A07 Identification & Authentication Failures** — weak session management, credential stuffing exposure, missing MFA. Fix: strong session tokens, rotation, lockout/throttling, MFA.
- **A08 Software & Data Integrity Failures** — unsigned updates, insecure deserialization. Fix: verify signatures, avoid native deserialization of untrusted data, integrity checks in CI/CD.
- **A09 Logging & Monitoring Failures** — no audit trail, secrets in logs. Fix: structured security logging (no secrets/PII), alerting on auth anomalies.
- **A10 SSRF** — server fetches a user-supplied URL. Fix: allowlist destinations, block link-local/metadata IPs, no redirects to internal ranges.

## PHP-specific secure patterns

- Database: always PDO/ORM **prepared statements** with bound parameters; never interpolate into SQL.
- Output: escape with the context-correct encoder (`htmlspecialchars` for HTML, never trust `|raw` in templates).
- Files/Uploads: validate MIME + extension allowlist, store outside webroot, randomize names, never `include`/`require` a user-controlled path.
- Deserialization: avoid `unserialize()` on untrusted input; prefer JSON with a schema.
- Auth: `password_hash()` (argon2id/bcrypt) + `password_verify()`; regenerate session id on privilege change; CSRF tokens on state-changing routes.
- Command exec: avoid `exec`/`shell_exec` on untrusted input; if unavoidable, use `escapeshellarg` + an allowlist.

## Go-specific secure patterns

- SQL: `database/sql` with placeholders (`$1`/`?`); never `fmt.Sprintf` a query.
- Templates: `html/template` (context-aware auto-escaping), not `text/template`, for HTML.
- Crypto: `crypto/rand` for tokens (never `math/rand`); `golang.org/x/crypto/bcrypt` or argon2 for passwords; `crypto/subtle.ConstantTimeCompare` for secret comparison.
- HTTP: set timeouts, validate/clean paths with `filepath.Clean` + descendant checks, restrict outbound SSRF targets.
- Supply chain: run `govulncheck ./...` in CI; keep `go.mod` patched.
- Deserialization/parsing: cap sizes (`io.LimitReader`), validate before use.

## Reviewer workflow

1. Map trust boundaries (where untrusted input enters).
2. For each input, trace to its sink (query, command, file, HTML, redirect).
3. Match to a vuln class above; confirm with a Tier-1 check where possible (static analyzer, `govulncheck`, `composer audit`, a focused test).
4. Produce: the finding, the secure fix, and a regression test that fails on the vulnerable version and passes on the fix.
