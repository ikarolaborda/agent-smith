# PHP Legacy Modernization: PHP 5.x and 7.x to Modern PHP 8.x

Modernization is a sequence of controlled compatibility changes, not a syntax
rewrite. Preserve proven business behavior, expose hidden dependencies, move
to supported runtimes and packages, and improve the architecture where change
risk justifies it.

Do not combine a runtime upgrade, framework replacement, database redesign,
and complete domain rewrite into one release. Each dimension needs evidence,
observability, rollback, and a bounded blast radius.

## 1. Define the Target and Constraints

Choose a currently supported PHP 8.x release from the official PHP supported
versions page. Record:

- current and target PHP versions and SAPIs;
- operating systems, architecture, container base, and web server;
- framework, Composer, extensions, database client, and native libraries;
- deployment topology, worker lifetime, cron jobs, and queues;
- availability, latency, and maintenance-window requirements;
- regulated or sensitive data;
- rollback and database compatibility requirements.

Pin a specific target in CI and production images. “PHP 8” is too broad for a
migration plan because minor releases introduce features, deprecations, and
behavior changes.

### Inventory before changing code

Capture:

- all web entry points, CLI commands, workers, scheduled jobs, and callbacks;
- enabled extensions from CLI and production SAPI;
- Composer and manually copied dependencies;
- autoloaders, include-path behavior, bootstrap files, and global constants;
- uses of superglobals, sessions, filesystem paths, environment variables,
  shell commands, mail, network calls, and local services;
- database schemas, stored procedures, triggers, SQL modes, encodings, and
  transaction assumptions;
- generated files, uploads, caches, and shared mutable state;
- production request, error, and latency baselines.

CLI and web SAPIs can load different configuration files and extensions.
Compare their effective configuration rather than assuming they match.

## 2. Establish a Safety Net

Legacy systems often encode requirements only in behavior. Characterization
tests capture that behavior before internal changes.

### Select stable observation boundaries

Useful boundaries include:

- HTTP request to status, headers, normalized body, and durable side effects;
- CLI arguments and input to exit code, output, and side effects;
- queue message to acknowledgements, emitted messages, and database changes;
- domain operation to return value and state transition;
- database fixture to generated report;
- scheduled job to idempotency marker and resulting records.

Do not snapshot volatile identifiers, timestamps, unordered collections, or
entire HTML pages without normalization. Broad snapshots can approve accidental
behavior and make reviews unreadable.

### Control nondeterminism

Introduce seams for:

- clock and timezone;
- random and identifier generation;
- filesystem and process environment;
- outbound HTTP, mail, DNS, and queues;
- database connection and transaction boundary;
- current user, tenant, locale, and feature flags.

Record the real behavior first, including odd but relied-upon behavior. Classify
each captured behavior as:

- required and must remain compatible;
- defective and intentionally changed with an approved test;
- obsolete and removable after usage evidence;
- unknown and requiring product or domain review.

### Build a representative test corpus

Include:

- normal production-shaped inputs;
- empty, null, zero, false, and boundary values;
- malformed and unauthorized requests;
- duplicate delivery and repeated form submission;
- timezone, encoding, and locale edge cases;
- database conflict, timeout, and partial dependency failure;
- representative historical defects.

Use sanitized or synthetic fixtures. Production secrets and personal data do
not belong in the test suite.

### Dual-runtime CI

When dependencies allow it, run the same characterization suite against the
old and target runtimes. Compare observable behavior, not warning text alone.
Turn target-runtime deprecations into tracked work. A temporary compatibility
matrix can include an intermediate PHP version when a direct jump makes
diagnosis too ambiguous.

## 3. Migration Hazards by Runtime Generation

Read every official migration appendix between the source and target; the list
below is a triage guide, not a substitute.

### PHP 5.x to PHP 7.x

High-risk areas include:

- removal of the original mysql extension; migrate to PDO or mysqli with
  prepared statements;
- removal of ereg and other long-deprecated APIs;
- changed variable and expression evaluation under uniform variable syntax;
- old-style same-name constructors, which must become __construct;
- code that relies on warnings, notices, or recoverable errors rather than
  Throwable handling;
- invalid calls whose behavior became stricter;
- extensions or vendor libraries without PHP 7-compatible releases;
- assumptions about integer size, string offsets, reference behavior, or
  foreach mutation.

PHP 7 introduced scalar and return type declarations and a Throwable hierarchy.
Add types after observed input has been cleaned up; otherwise type declarations
can convert hidden data defects into outages.

### PHP 7.x transitions

Across PHP 7 minor releases, inspect:

- count calls on non-countable values;
- deprecated each and create_function usage;
- object and array conversion assumptions;
- numeric-string and invalid-number handling;
- deprecated curly-brace array or string offsets;
- mbstring, JSON, PCRE, and database extension behavior;
- signature compatibility warnings in overridden methods;
- reliance on undefined variables, keys, or offsets.

Replace dynamic code generation with closures or named functions. Treat new
warnings as defects to understand, not output to suppress globally.

### PHP 7.4 to PHP 8.0

PHP 8 intentionally made many formerly permissive cases fail more clearly.
Review:

- removed functions and deprecated syntax;
- stricter internal function argument validation;
- warnings promoted to Error or TypeError;
- changed string-to-number and loose-comparison behavior;
- undefined variable, array-key, and property access;
- arithmetic and concatenation precedence;
- method signature compatibility;
- resources represented as objects by some extensions;
- named arguments, because public parameter names become part of the calling
  contract;
- error handlers that assume all failures are ordinary exceptions.

New features such as union types, attributes, constructor property promotion,
match, and the null-safe operator improve clarity, but adopt them after
compatibility is proven. Do not mix every available syntax change into the
runtime cutover.

### Modern PHP 8.x

Later PHP 8 releases add capabilities such as enums, readonly properties and
classes, intersection and disjunctive-normal-form types, fibers, typed class
constants, override metadata, property hooks, and asymmetric property
visibility. They also continue to deprecate ambiguous behavior.

Common review areas include:

- dynamic properties, deprecated from PHP 8.2 except for defined compatibility
  cases;
- tentative and native return types on internal interfaces;
- implicit nullable declarations and other signature ambiguities;
- serialization of typed, readonly, or enum-backed state;
- reflection and proxy libraries that generate or mutate properties;
- framework, ORM, test-runner, and mocking-tool support for the exact target.

Prefer an explicit declared property or a deliberate key-value structure over
silencing dynamic-property warnings. Verify current release details in the
exact official migration guide.

## 4. Find and Create Dependency Seams

A seam is a place where behavior can be changed or substituted without editing
every caller.

### Typical legacy coupling

- global variables and superglobals read deep in business logic;
- static service access and singleton registries;
- direct new construction of database, HTTP, mail, or filesystem clients;
- include files that perform work as a side effect;
- Active Record objects that mix persistence, validation, and domain rules;
- controllers that parse input, execute SQL, apply business rules, and render;
- functions whose output depends on time, locale, session, or process state;
- framework objects passed throughout the domain.

### Seam-building sequence

1. Put a characterization test around the current boundary.
2. Extract the smallest volatile operation behind an application-owned
   function or interface.
3. Preserve the old implementation as the first adapter.
4. Pass the dependency explicitly through a constructor or function argument.
5. Move wiring to a composition root.
6. Add a contract test shared by old and replacement adapters.
7. Replace callers incrementally.

Examples of useful ports are Clock, PaymentGateway, UserRepository,
PasswordHasher, Mailer, TransactionManager, and AuditLog. Name them in domain
terms, not after a vendor SDK.

### Branch by abstraction

When old and new implementations must coexist:

- introduce a stable port in front of the old behavior;
- route all callers through it;
- implement the new path behind the same contract;
- compare results in shadow mode where safe;
- switch traffic by tenant, feature, or request class;
- retain a bounded rollback window;
- remove the old path and temporary flag after evidence.

The abstraction is temporary only if ownership and removal criteria are
explicit.

## 5. Composer and Dependency Hygiene

Composer makes dependency and autoloading behavior reproducible when used
deliberately.

### Baseline package practice

- Define the minimum supported PHP version and required extensions.
- Use PSR-4 autoloading for project namespaces.
- Commit composer.lock for applications.
- Install production builds without development dependencies and with an
  optimized, authoritative autoloader only after verifying dynamic loading
  needs.
- Run composer validate and composer audit in CI.
- Inspect abandoned packages and unmaintained transitive dependencies.
- Keep credentials out of composer.json and repository configuration.
- Verify package provenance and minimize custom installer/plugin execution.

Composer's config.platform can help dependency resolution model a target
runtime, but it does not run or test that runtime. CI must execute on the real
PHP version and required extensions.

### Replacing copied vendor code

Do not immediately swap an unknown modified library for the latest package.
First:

1. identify local modifications;
2. capture caller behavior and security assumptions;
3. locate the canonical package and compatible versions;
4. compare licenses and transitive dependencies;
5. wrap the current implementation behind a seam;
6. replace and contract-test it;
7. remove the copied code after rollout.

### PSR standards as interoperability contracts

Useful PHP-FIG recommendations include:

- PSR-1 and PSR-12 for baseline coding style;
- PSR-4 for class autoloading;
- PSR-3 for logging;
- PSR-6 or PSR-16 for caching;
- PSR-7 and PSR-17 for HTTP messages and factories;
- PSR-11 for container interoperability;
- PSR-15 for HTTP server middleware;
- PSR-18 for HTTP clients.

Adopting a PSR interface can reduce framework lock-in. Do not inject a PSR-11
container throughout the application as a service locator; inject the actual
dependencies.

## 6. Introduce Types Without Hiding Data Problems

Type migration is a data-quality project as much as a code change.

Recommended order:

1. document observed null, false, empty-string, numeric-string, and array shapes;
2. validate and normalize external input at the boundary;
3. replace sentinel values with explicit result or exception semantics;
4. add parameter and return types to stable internal code;
5. add property types and immutable value objects;
6. raise static-analysis strictness in bounded modules;
7. prevent new violations while burning down the baseline.

Avoid broad coercion at every caller. Normalize once, close to the untrusted or
legacy boundary, and keep the internal representation precise.

Static analysis complements runtime tests. Configure a baseline for existing
findings, but fail CI on new findings and shrink the baseline over time. Review
suppression comments; each should explain why the code is safe.

## 7. Error Handling and Observability

### Errors

- Catch Throwable at process or request boundaries for logging and controlled
  failure; catch narrower domain or infrastructure exceptions where recovery
  is possible.
- Preserve the previous exception as the cause when translating boundaries.
- Do not expose stack traces, SQL, filesystem paths, secrets, or internal
  identifiers to clients.
- Use finally for cleanup and transaction rollback where ownership requires it.
- Make retry decisions from error semantics, not message strings.

### Long-running workers

Classic request-based PHP releases most request memory at the end of the
request. Queue workers, application servers, and persistent runtimes do not.
They must:

- reset request-scoped state;
- close or recycle connections;
- avoid static caches that grow forever;
- respond to cancellation and deployment signals;
- cap jobs or lifetime and restart predictably;
- emit per-job memory and latency metrics.

### Migration telemetry

Track by old/new path and runtime version:

- request and job counts;
- status/error classes;
- latency percentiles;
- memory and CPU;
- database query count and slow queries;
- queue lag, retries, and duplicate suppression;
- business outcomes such as orders completed or invoices issued.

Logs need correlation identifiers and must avoid credentials, session tokens,
and sensitive payloads.

## 8. Security Modernization

An unsupported runtime or package is a security defect. Runtime migration does
not by itself fix application vulnerabilities.

### Input and output

- Validate shape, size, encoding, and allowed values at trust boundaries.
- Use context-specific output encoding; HTML encoding is not correct for every
  JavaScript, URL, CSS, or header context.
- Keep templates escaped by default and review raw-output exceptions.
- Reject unexpected array shapes and duplicate parameter ambiguity.

### Database access

- Use PDO or mysqli prepared statements with bound values.
- Identifiers such as table or column names cannot be safely parameterized;
  choose them from an allowlist.
- Use least-privilege database accounts.
- Make transaction boundaries explicit and preserve authorization predicates
  in reads and writes.

### Authentication and sessions

- Hash passwords with password_hash and verify with password_verify; let PHP
  select supported password algorithms and rehash when parameters change.
- Regenerate session identifiers on authentication and privilege changes.
- Set Secure, HttpOnly, and an appropriate SameSite policy on cookies.
- Apply CSRF protection to state-changing browser requests.
- Enforce authorization server-side for every object and action.
- Rate-limit authentication and recovery flows without creating a trivial
  denial of service.

### Dangerous boundaries

- Avoid unserialize for untrusted data; use a validated structured format.
- Avoid shell execution. When it is unavoidable, use fixed executables,
  argument arrays where supported, allowlisted values, timeouts, and a
  restricted OS identity.
- Store uploads outside executable web roots, generate server-side names,
  enforce size and type policy, and serve with safe content headers.
- For outbound URLs, allowlist destinations or enforce scheme, DNS/IP, redirect,
  and private-address policy to reduce SSRF risk.
- Disable unsafe XML external entity and network resolution behavior.
- Keep secrets in a managed runtime source, rotate them, and prevent logging.

Run dependency, static, secret, and container scans, but validate findings and
add regression tests for confirmed vulnerabilities.

## 9. Strangler Modernization

The strangler pattern replaces capabilities around the edges while the old
system remains operational.

### Select a slice

Prefer a capability with:

- clear inputs and outputs;
- measurable value or risk reduction;
- limited shared database mutation;
- enough production traffic to validate;
- an independent rollback path.

A thin vertical slice is better than replacing an entire technical layer.

### Route and translate

Place a controlled routing boundary at the web server, gateway, queue, or
application facade. An anti-corruption layer translates legacy vocabulary,
identifiers, errors, and data shapes into the new model. Do not let the new
domain inherit the old database schema as its public API.

### Data ownership

Assign one authoritative writer for each datum. Avoid uncoordinated dual writes.
Useful transition mechanisms include:

- old system remains writer while the new path reads through an adapter;
- new system becomes writer and publishes changes through an outbox;
- change capture populates a derived read model;
- a bounded backfill migrates ownership;
- reconciliation detects divergence.

If temporary dual writing is unavoidable, define failure recovery,
idempotency, ordering, and reconciliation before rollout.

### Expand-and-contract schemas

1. Add backward-compatible schema.
2. Deploy code able to coexist with old and new representations.
3. Backfill in resumable batches.
4. Compare counts, checksums, invariants, and business outcomes.
5. switch reads, then writes, under an observable control;
6. wait through the rollback and old-deployment window;
7. remove obsolete columns, tables, and compatibility code.

Do not assume a code rollback can reverse a migrated database.

## 10. A Staged Delivery Plan

### Stage 0: stabilize

- freeze untracked platform changes;
- establish ownership and incident contacts;
- capture production baselines and backups;
- prove restore and rollback;
- remove secrets from source control.

### Stage 1: characterize

- map entry points and side effects;
- add boundary characterization tests;
- reproduce high-risk defects;
- inventory extensions and dependencies;
- run static analysis and target-runtime deprecation discovery.

### Stage 2: make the build reproducible

- adopt Composer and PSR-4;
- pin runtime, extensions, OS packages, and dependencies;
- make configuration explicit;
- build immutable artifacts in CI.

### Stage 3: create seams

- isolate database, clock, filesystem, HTTP, mail, and session access;
- move wiring to a composition root;
- introduce contract tests;
- reduce global and static mutable state.

### Stage 4: bridge runtime versions

- resolve official migration-guide incompatibilities;
- update or replace unsupported dependencies;
- run dual-runtime tests;
- deploy target runtime to a canary with observability.

### Stage 5: improve internals

- normalize boundaries and add types;
- extract domain invariants from controllers and persistence;
- adopt modern language features where they clarify the model;
- raise static-analysis and test quality gates.

### Stage 6: strangle bounded capabilities

- route selected traffic to new slices;
- compare outcomes;
- transfer data ownership deliberately;
- remove old code, flags, dependencies, and infrastructure after the rollback
  window.

## 11. Exit Criteria for Each Increment

- Characterization and regression tests pass.
- The exact old/new compatibility matrix is documented.
- Security checks and dependency audit have no unaccepted critical findings.
- Production-like load and memory behavior meet thresholds.
- Schema and deployment order support mixed versions.
- Observability distinguishes old and new behavior.
- Backup, rollback, and forward-fix paths are tested.
- Product owners approve intentional behavior changes.
- Temporary flags, adapters, and baselines have owners and removal dates.

## Stable Primary References

- **[Official lifecycle policy]** PHP Project,
  [Supported Versions](https://www.php.net/supported-versions.php).
- **[Official migration guide]** PHP Project,
  [Migrating from PHP 5.6.x to PHP
  7.0.x](https://www.php.net/manual/en/migration70.php).
- **[Official migration guides]** PHP Project,
  [PHP 7.0 through PHP 8.x migration
  appendices](https://www.php.net/manual/en/appendices.php).
- **[Official language manual]** PHP Project,
  [Language Reference](https://www.php.net/manual/en/langref.php) and
  [Security](https://www.php.net/manual/en/security.php).
- **[Official dependency manager]** Composer,
  [Basic Usage](https://getcomposer.org/doc/01-basic-usage.md),
  [Schema](https://getcomposer.org/doc/04-schema.md), and
  [Command Line Interface](https://getcomposer.org/doc/03-cli.md).
- **[Primary interoperability standards]** PHP-FIG,
  [Accepted PSRs](https://www.php-fig.org/psr/).
- **[Primary PSR-4 specification]** PHP-FIG,
  [Autoloader](https://www.php-fig.org/psr/psr-4/).
- **[Primary coding-style specification]** PHP-FIG,
  [PSR-12](https://www.php-fig.org/psr/psr-12/).
- **[Security verification standard]** OWASP,
  [Application Security Verification
  Standard](https://owasp.org/www-project-application-security-verification-standard/).
- **[Security testing guide]** OWASP,
  [Web Security Testing Guide
  v4.2](https://owasp.org/www-project-web-security-testing-guide/v42/).

Release-specific PHP manuals and package documentation are authoritative for
compatibility. Migration tools and static analyzers accelerate discovery but
do not replace execution on the real target runtime.
