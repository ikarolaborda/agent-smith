# Software Engineering: Design, Architecture, Testing, and Evolution

This manual is a compact decision guide for building and changing non-trivial
software. It treats design principles as tools, not laws. Apply a principle
when it reduces the cost or risk of change; do not add abstraction merely to
make a diagram look complete.

## 1. Start With the Problem and Its Forces

Before choosing classes, frameworks, services, or databases, write down:

- the user or business outcome;
- the invariants that must always hold;
- the important quality attributes: security, reliability, latency,
  throughput, availability, operability, portability, and modifiability;
- expected load, data volume, failure modes, and regulatory constraints;
- what is likely to change and what must remain stable;
- the team's delivery and operational capabilities.

Architecture is the set of consequential decisions that are costly to reverse.
It is not synonymous with microservices, layers, or a particular directory
layout. A good architecture makes important behavior easy to locate, permits
likely changes without widespread edits, and makes failure visible.

### Functional requirements versus quality attributes

A functional requirement says what the system does. A quality-attribute
scenario makes a property testable:

- source of stimulus: who or what causes the event;
- stimulus: the event;
- environment: normal load, peak load, degraded mode, or recovery;
- artifact: the component or data affected;
- response: what the system must do;
- measure: an observable threshold.

Example: during normal load, when a dependency times out, the checkout API
returns a bounded error within two seconds, records a correlation identifier,
and does not create a duplicate order. This is more useful than “the system
must be resilient.”

## 2. Object-Oriented Design

Object orientation is most useful when objects protect meaningful state and
behavior. A class that only exposes getters and setters is usually a record,
not a domain abstraction.

### Core concepts

- **Encapsulation** keeps an invariant and the operations that preserve it
  together. It is about controlling valid state transitions, not making every
  field private by ritual.
- **Abstraction** exposes the concepts a caller needs while hiding irrelevant
  mechanism.
- **Polymorphism** lets callers depend on a behavioral contract rather than a
  concrete implementation.
- **Inheritance** reuses and specializes a contract. It creates strong
  coupling and is appropriate only for a genuine substitutable “is-a”
  relationship.
- **Composition** builds behavior from collaborating objects. Prefer it when
  capabilities vary independently or at runtime.

### Entities, value objects, and services

- An **entity** has continuity through time and an identity independent of its
  current attributes. Equality normally follows identity.
- A **value object** is defined by its values, is ideally immutable, validates
  itself at construction, and can be freely replaced by an equal value.
- A **domain service** represents domain behavior that does not naturally
  belong to one entity or value object.
- An **application service** coordinates a use case, transactions, and ports.
  It should not become the home of every business rule.

Model invalid states out where practical. A Money value should not combine an
amount and currency without validation. An Order should not expose arbitrary
status assignment if only specific transitions are legal.

### Collaboration guidance

- Tell an object to perform a domain operation instead of extracting its state
  and making decisions elsewhere.
- Pass the smallest capability a collaborator needs.
- Keep mutation local; immutable values make sharing and reasoning easier.
- Avoid deep navigation chains that expose internal object graphs.
- Use inheritance only when every subtype honors the base type's behavioral
  contract.
- Avoid “manager,” “helper,” and “util” classes that collect unrelated
  responsibilities.

## 3. SOLID, With Failure Modes

SOLID is a vocabulary for coupling and change. It does not require an
interface for every class.

### Single Responsibility Principle

A module should have one coherent reason, or one primary actor, that causes it
to change. “Does one thing” is too literal: a cohesive module can contain many
operations that serve the same responsibility.

Signals of a violation:

- a class changes for pricing policy, database schema, and presentation;
- unrelated tests fail when one concern changes;
- different teams repeatedly edit the same module for unrelated reasons.

Split by axis of change, not by line count.

### Open/Closed Principle

Stable policy should be extendable without repeatedly modifying a risky
central conditional. Useful mechanisms include polymorphism, data-driven
rules, plugins, and explicit dispatch tables.

Do not predict every future extension. First allow duplication to reveal a
stable variation point; then extract the abstraction. Premature extension
points create a framework nobody needs.

### Liskov Substitution Principle

Any implementation of a contract must be usable wherever that contract is
expected without surprising the caller.

A subtype or implementation must not:

- require stronger preconditions;
- provide weaker postconditions;
- violate invariants promised by the contract;
- throw new categories of errors callers cannot reasonably handle;
- change semantic meaning, such as turning an ordered collection into an
  unordered one.

Mocks that satisfy a method signature but not its behavior can hide Liskov
violations. Contract tests should run against every adapter implementation.

### Interface Segregation Principle

Clients should depend only on operations they use. Small, role-oriented
interfaces reduce coupling and make tests clearer. Interfaces should normally
be owned near the consumer, because the consumer defines the needed contract.

An interface with one implementation is not automatically wrong, but it needs
a boundary reason: test isolation, volatile infrastructure, alternate
implementations, or policy/mechanism separation.

### Dependency Inversion Principle

High-level policy should not depend directly on low-level mechanisms; both
should meet at an abstraction shaped by policy needs.

Dependency inversion does not mean using a dependency-injection container
everywhere. Constructor injection and a composition root are usually enough.
Avoid service locators because they hide dependencies and defer errors until
runtime.

## 4. Clean Code as Local Reasoning

Clean code minimizes the context a reader must load to make a safe change.

### Names and functions

- Use domain language and distinguish concepts precisely.
- Name booleans as predicates and commands as actions.
- Keep a function at one conceptual level.
- Prefer explicit inputs and outputs over ambient global state.
- Separate decisions from side effects when that improves testability.
- Replace comments that restate code with clearer code.
- Preserve comments that explain constraints, trade-offs, hazards, or why an
  apparently simpler approach is incorrect.

Small functions are useful when they expose intent. Fragmenting a linear
operation into many one-line functions can make control flow harder to follow.

### Errors and resource ownership

- Add context at boundaries while preserving the original cause.
- Distinguish validation, conflict, unavailable dependency, timeout, and
  internal failure when callers need different actions.
- Never silently swallow an error.
- Make retries bounded and idempotency-aware.
- Establish who owns closing files, releasing locks, ending transactions, and
  cancelling background work.
- Do not log the same error at every layer; enrich it while propagating, then
  log once where it is handled.

### Duplication and abstraction

Not all similar code has the same reason to change. Duplicating a few lines can
be safer than coupling two unrelated concepts. Extract when the shared rule is
stable and the call sites share semantics, not merely syntax.

Good abstractions:

- have a small, explicit contract;
- hide volatile details;
- make the common case obvious;
- allow errors and limits to be represented;
- do not leak infrastructure types into the domain.

## 5. Clean Architecture and Ports

Clean Architecture, hexagonal architecture, and onion architecture share a
central idea: business policy should not depend on delivery, storage, or
vendor mechanisms.

### Dependency rule

Source-code dependencies point inward toward stable policy:

1. **Domain**: entities, value objects, invariants, domain services.
2. **Application**: use cases, orchestration, transaction boundaries, input
   and output ports.
3. **Adapters**: HTTP handlers, CLI commands, message consumers, repositories,
   external clients, presenters.
4. **Frameworks and drivers**: databases, web frameworks, brokers, filesystem,
   cloud SDKs.

This is a dependency rule, not a requirement for four deployables or four
folders. Small applications may combine layers while preserving the direction.

### Boundary rules

- A driving adapter translates an external request into a use-case input.
- A driven adapter implements a capability requested by application policy.
- Domain code does not import HTTP, SQL, ORM, queue, or framework types.
- Translate transport and persistence records at boundaries.
- Define transaction ownership in the application layer; do not scatter
  commits across repositories.
- Keep the composition root near process startup, where concrete adapters are
  wired to ports.

### What should cross a boundary

Use stable, application-owned data structures. Avoid passing an ORM entity,
HTTP request, database row, or vendor exception deep into policy. Boundary
translation has a cost, but it prevents infrastructure semantics from taking
over the model.

Do not create mappings for their own sake. If a simple read-only utility has
no business policy and little expected change, direct framework use may be the
more honest design.

## 6. Architecture Decisions and Trade-offs

Choose the simplest architecture that satisfies measured forces and preserves
an acceptable path to change.

### Modular monolith versus microservices

Prefer a modular monolith when:

- the domain boundaries are still being discovered;
- one team owns most behavior;
- independent scaling or deployment is not required;
- strong transactions simplify important invariants;
- operational maturity is limited.

Consider microservices when independently owned bounded contexts need
independent deployment, scaling, availability, or technology choices, and the
organization can operate distributed systems.

Costs of services include network failure, latency, versioned contracts,
eventual consistency, duplicated operational tooling, incident coordination,
and data ownership. A set of processes that must always deploy together is a
distributed monolith.

### Synchronous versus asynchronous collaboration

Synchronous calls provide immediate results and simple request tracing, but
couple availability and latency across the call chain.

Asynchronous messaging supports buffering, fan-out, and temporal decoupling,
but introduces duplicate delivery, ordering questions, lag, dead letters, and
eventual consistency. Consumers must be idempotent. Use a transactional outbox
when a database change and event publication must not diverge.

### Relational versus non-relational storage

Start with a relational database when relationships, constraints,
transactions, and flexible querying matter. Choose a specialized store only
for a demonstrated access pattern or operational need:

- document stores for aggregate-shaped documents with controlled denormalized
  duplication;
- key-value stores for bounded lookup by key;
- time-series stores for high-volume timestamped measurements;
- search indexes for relevance-ranked text retrieval;
- graph stores for deep relationship traversal.

Using several stores creates backup, consistency, security, and operational
work. A cache and search index are derived data unless explicitly designed as
systems of record.

### CQRS and event sourcing

CQRS can help when write invariants and read shapes differ substantially. It
adds synchronization and consistency concerns. Apply it to a bounded context,
not automatically to an entire product.

Event sourcing is appropriate when the event history is itself the record,
auditability and temporal reconstruction are central, and the team can manage
event schema evolution and replay. It is not a generic replacement for a
database audit table.

### Caching

Every cache needs answers to:

- what is the source of truth;
- how entries are keyed and isolated by tenant;
- when entries expire or are invalidated;
- whether stale data is acceptable;
- how stampedes and negative results are handled;
- what happens when the cache is unavailable.

Cache only after measuring. Invalidation and coherence are part of the design,
not cleanup work.

### Build versus buy

Buy or adopt a maintained component when the capability is not differentiating
and integration risk is lower than ownership risk. Build when requirements are
core, available products cannot meet them, and the organization accepts the
full lifecycle: security, upgrades, support, observability, and migration.

## 7. Architecture Decision Records

Record decisions whose context would otherwise be lost. A useful ADR contains:

- title and status;
- date, owners, and decision scope;
- context and quality-attribute forces;
- options considered, including “do nothing”;
- decision and rationale;
- positive and negative consequences;
- validation signals and a review trigger.

Avoid retroactive certainty. An ADR should reveal the information available at
the time. Supersede an old ADR rather than rewriting history.

For high-risk decisions, test assumptions with a thin vertical prototype,
load test, failure injection, or architecture evaluation before committing.

## 8. Testing Strategy

Tests provide evidence at different boundaries; no single test type is enough.

### Test portfolio

- **Unit tests** exercise deterministic policy in memory and should be fast
  and precise.
- **Component tests** exercise a deployable unit through a public boundary,
  often with real internal adapters and controlled external dependencies.
- **Integration tests** verify assumptions about databases, brokers,
  filesystems, SDKs, and protocols.
- **Contract tests** verify provider/consumer compatibility and should run
  against every implementation of a port.
- **End-to-end tests** validate a small set of critical user journeys in a
  production-like environment.
- **Property-based tests** generate cases and check invariants, useful for
  parsers, state machines, serialization, and numerical logic.
- **Mutation testing** checks whether the suite detects deliberately changed
  behavior; it reveals assertions that execute code but prove little.
- **Performance and resilience tests** validate quality-attribute scenarios,
  including overload, dependency failure, recovery, and resource exhaustion.

Use real dependencies where their semantics are the subject of the test. A
mocked database cannot prove transaction isolation or SQL compatibility.

### Test doubles

- A **stub** returns predetermined data.
- A **fake** provides a working but simplified implementation.
- A **spy** records interactions.
- A **mock** verifies expected interactions.

Prefer assertions on observable behavior. Interaction assertions are justified
when the interaction itself is the contract, such as emitting an audit event.
Excessive mocking of internal methods locks tests to implementation structure
and makes safe refactoring harder.

### Characterization and regression tests

Before changing unfamiliar or legacy behavior:

1. capture representative inputs and outputs at stable boundaries;
2. control time, randomness, network, and filesystem effects;
3. record database and message side effects;
4. include known edge cases and failure behavior;
5. review snapshots to ensure secrets and accidental behavior are not blessed;
6. change one seam at a time.

A regression test should fail for the original defect and pass for the fix.
Testing only the new implementation can miss whether the defect was reproduced.

## 9. Safe Evolution

### Compatibility

- Treat public APIs, events, database schemas, configuration, and operational
  behavior as contracts.
- Prefer additive changes before removals.
- Use tolerant readers carefully; do not silently ignore fields whose meaning
  is security-sensitive.
- Version contracts when semantic compatibility cannot be preserved.
- Publish deprecation and removal criteria, then measure actual usage.

### Database expand-and-contract

For a no-downtime incompatible schema change:

1. expand with a backward-compatible table, column, or index;
2. deploy code that can operate during the transition;
3. backfill in bounded, resumable batches;
4. validate counts, invariants, and lag;
5. switch reads or ownership behind an observable control;
6. stop old writes;
7. contract only after all old versions and rollback windows are gone.

Avoid long blocking migrations in a deployment transaction. Test migration
duration and locking behavior on production-scale data.

### Operational evolution

- Use feature flags for controlled exposure, not as permanent hidden branches.
- Make rollback behavior explicit; a code rollback may not reverse a data
  migration.
- Instrument latency, errors, saturation, queue lag, and domain outcomes.
- Use correlation identifiers across boundaries.
- Define service-level objectives from user-visible behavior.
- Conduct blameless incident reviews that produce owned, testable follow-up
  work.

## 10. Review Checklists

### Design review

- Are invariants explicit and enforced in one authoritative place?
- Is the boundary aligned with a domain or operational reason?
- Do dependencies point toward stable policy?
- Are failure, timeout, retry, cancellation, and idempotency defined?
- Is sensitive data minimized and access auditable?
- Are resource and scaling limits explicit?
- Can the design be tested through public behavior?
- Is the complexity justified by a current requirement or measured risk?

### Change review

- What contract changes, including implicit operational contracts?
- What old and new versions can coexist during deployment?
- What is the data migration and rollback story?
- Which tests prove the intended behavior and failure behavior?
- Which metrics show success or harm after release?
- Who owns the component and its incidents?
- What decision should be recorded or revisited?

## Stable and Canonical References

- **[Consensus body of knowledge]** IEEE Computer Society, [SWEBOK Guide
  V4](https://www.computer.org/education/bodies-of-knowledge/software-engineering/v4).
- **[Architecture description standard]** ISO/IEC/IEEE 42010,
  [Systems and software engineering — Architecture
  description](https://www.iso.org/standard/74393.html).
- **[Quality model standard]** ISO/IEC 25010,
  [Systems and software quality
  models](https://www.iso.org/standard/78176.html).
- **[Primary SEI method]** Carnegie Mellon Software Engineering Institute,
  [Architecture Tradeoff Analysis
  Method](https://www.sei.cmu.edu/library/architecture-tradeoff-analysis-method-atam/).
- **[Canonical principles]** Robert C. Martin, Agile Software Development:
  Principles, Patterns, and Practices; Clean Code; and Clean Architecture.
  These are influential practitioner sources, not formal standards.
- **[Primary architecture essays]** Martin Fowler,
  [Software Architecture Guide](https://martinfowler.com/architecture/).
- **[Primary pattern source]** Alistair Cockburn,
  [Hexagonal Architecture](https://alistair.cockburn.us/hexagonal-architecture/).
- **[Testing vocabulary standard]** ISO/IEC/IEEE 29119,
  [Software testing](https://www.iso.org/standard/81291.html).
- **[Delivery research]** DORA,
  [Research program and metrics](https://dora.dev/research/).

Standards define shared vocabulary and minimum structure; they do not replace
context-specific engineering judgment.
