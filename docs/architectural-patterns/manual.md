# Architectural Patterns — Selected Documentation

Sources: context7 `/websites/martinfowler` (martinfowler.com) and `/websites/refactoring_guru_design-patterns`.

## Hexagonal Architecture (Ports and Adapters)

Coined by Alistair Cockburn. The application's core (domain + use cases) is isolated from external concerns (UI, database, external services) by **ports** (interfaces the core defines) and **adapters** (implementations of those interfaces).

Key ideas:
- **Driving (primary) adapters** invoke the core — HTTP controllers, CLI handlers, message consumers.
- **Driven (secondary) adapters** are invoked by the core — DB repositories, HTTP clients, email senders.
- The core depends on ABSTRACTIONS only (the ports). Adapters depend on the core.
- Dependencies always point inward: adapter → port → core. The core never imports an adapter.

Typical layout:
```
app/
  core/
    domain/           ← entities, value objects
    usecases/         ← application services orchestrating domain
    ports/
      in/             ← interfaces driving adapters call
      out/            ← interfaces driven adapters implement
  adapters/
    http/             ← REST/gRPC controllers (driving)
    persistence/      ← repositories (driven)
    messaging/        ← brokers (driving or driven, depending on direction)
```

Benefits:
- Testable: swap real adapters for in-memory fakes; the core is exercised through its ports.
- Replaceable infrastructure: change Postgres → DynamoDB without touching the core.
- Clear contract: the port enumerates exactly what the core needs.

Trade-offs:
- More files and indirection up-front; obvious win only on non-trivial systems.
- Requires discipline to avoid "leak through" (importing adapter types in the core).

## Event-Driven Architecture (EDA)

Martin Fowler distinguishes several patterns under the EDA umbrella:

1. **Event Notification** — a service emits "X happened" without expecting a particular consumer. Subscribers act independently. Loose coupling, but reasoning across services requires tracing.
2. **Event-Carried State Transfer** — events include the data consumers need so they don't have to call back to the source. Eliminates synchronous fan-out; consumers cache their slice of state.
3. **Event Sourcing** — application state is the fold of an append-only event log. The current state is derived; the events ARE the system of record. Enables time-travel queries, full audit, and replay.
4. **CQRS (Command Query Responsibility Segregation)** — separate models for writes (commands) and reads (queries). Often paired with Event Sourcing, but viable alone.

When to use EDA:
- Cross-service workflows where one action triggers many downstream effects.
- Audit/compliance demands a full history.
- Read-heavy systems where a different denormalized model serves queries.

When NOT to use it:
- Simple CRUD applications. Naive EDA adds eventual-consistency complexity for no benefit.
- Teams not ready to invest in observability, dead-letter handling, and schema evolution.

CQRS guidance (Fowler):
> CQRS is a significant architectural pattern that should not be adopted unless the benefits clearly outweigh the complexity. Many systems naturally fit a CRUD model. CQRS is best applied to specific portions of a system, such as a Bounded Context in Domain-Driven Design, rather than the entire system.

## Microservices

Microservices decompose a system into small, independently deployable services owned by small teams. Each service:
- Owns its data store; other services cannot read it directly.
- Communicates via well-defined contracts (HTTP/gRPC sync, or async events).
- Can be deployed, scaled, and operated independently.

Prerequisites (Fowler, Newman):
- Mature DevOps: CI/CD, infrastructure as code, observability, on-call rotation.
- Bounded contexts already understood at the domain level.
- A "monolith first" history when in doubt — extracting services from a working monolith is safer than greenfield distributed design.

Common pitfalls:
- **Distributed monolith** — services that must be deployed together because their contracts are too tightly coupled.
- **Chatty boundaries** — too many synchronous calls; tail latencies compound.
- **Shared database** — defeats the point.
- **No service catalog / no ownership** — orphan services.

Communication patterns:
- **Sync**: HTTP/REST or gRPC. Best for request/response with low latency tolerance.
- **Async**: events on a broker (Kafka, RabbitMQ, NATS). Best for fan-out, decoupling, retries.
- **Saga** — long-running distributed transaction coordinated by either choreography (each service reacts to events) or orchestration (a central coordinator drives the steps).
- **Outbox pattern** — to avoid dual-write inconsistency, write the event and the row in the same DB transaction, then a relay publishes the outbox to the broker.

## Bounded Context (DDD)

A bounded context is a explicit boundary inside which a domain model is consistent. Different contexts can use the same word with different meanings ("customer" in Sales vs Billing). Microservices map well to bounded contexts — but the boundary, not the technology, is what matters.

## CAP and PACELC (Distributed Trade-offs)

- **CAP**: under partition, choose Consistency or Availability.
- **PACELC**: when Partitioned, choose A or C; Else, when running normally, choose Latency or Consistency.
- Most operational systems are AP under partition + tunable for L vs C under normal operation.

## Comparison: Hexagonal vs Microservices

These are orthogonal. Hexagonal is a structuring pattern WITHIN a deployable unit. Microservices is a decomposition pattern ACROSS deployable units. A single microservice can be internally hexagonal, and a monolith can be internally hexagonal too.

## Selection Heuristic

- Greenfield small/medium product → modular monolith with hexagonal cores.
- Mature product with team scaling pain → extract bounded contexts into services, one at a time.
- Audit/replay/temporal needs → consider event sourcing within the relevant context.
- Read/write asymmetry → consider CQRS within the relevant context.
- Cross-service workflows → event notification first; saga when ordering matters.

## Best Practices

- Make boundaries explicit (ports, contracts, schemas).
- Version your events; assume consumers will outlive producers.
- Make every async handler idempotent.
- Trace everything (OpenTelemetry, correlation IDs).
- Prefer choreography for simple flows; orchestration for >3 steps or human-in-the-loop.
- Keep the core (domain + use cases) free of framework or transport types.
