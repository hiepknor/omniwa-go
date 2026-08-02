# ADR 0056: Evolve as an active-passive modular monolith

- Status: Accepted
- Date: 2026-08-02

## Context

The application currently runs as one Go process with domain packages, shared
PostgreSQL persistence, WhatsApp session state, optional event transports, and
background workers. The composition root in `cmd/evolution-go/main.go` has
accumulated provider construction and lifecycle wiring. This makes startup
changes hard to review, but it does not by itself justify distributed services.

WhatsApp session ownership is stateful. Running two active application replicas
against the same instance can create duplicate connections, conflicting state,
and ambiguous command outcomes. The existing database ownership guard therefore
enforces one active process. Webhook and RabbitMQ deliveries are durable through
the PostgreSQL outbox, while NATS and WebSocket delivery have different
guarantees documented in `docs/EXTERNAL_EVENT_RELIABILITY.md`.

The fork must continue accepting upstream changes without moving domain packages,
changing the Go module path, or editing the licensed core and console assets.

## Decision

OmniWA GO remains a modular monolith. Process composition moves incrementally
from the entrypoint into focused files under `pkg/bootstrap`; domain behavior
stays in the existing `pkg/<domain>` packages. Each extraction must preserve API,
schema, configuration, and runtime semantics and must be independently
revertible.

High availability will first use active-passive application replicas with one
authoritative WhatsApp session owner. PostgreSQL is the coordination and durable
state boundary. Promotion is allowed only after the former active owner has lost
or released ownership. Automatic multi-active instance sharding is deferred
until ownership leases, fencing tokens, routing, and split-brain recovery have
been designed and tested.

Projection-backed reads remain the default where a projection contract exists.
Direct WhatsApp queries are reserved for explicit live operations and remain
bounded by the query guard. Webhook and RabbitMQ continue through the durable
outbox. WebSocket remains an ephemeral live channel, and NATS remains
best-effort until a separate durability decision is accepted.

The first implementation slice extracts external-event transport and outbox
worker construction into `pkg/bootstrap`. It does not alter worker registration,
transport behavior, database records, or public routes.

Initial engineering gates for an active-passive rollout are:

- a controlled failover drill reaches a healthy active process within five
  minutes;
- PostgreSQL-committed sessions, durable events, and outbox deliveries have zero
  intentional data loss during the drill;
- no instance is owned by two healthy processes at once;
- queued external deliveries resume and drain after promotion without manual row
  mutation;
- rollback to the last image digest remains possible without a schema rollback.

These are release gates, not a customer-facing service-level agreement.

## Alternatives

### Keep all composition in `main.go`

This minimizes the immediate diff but preserves a high-blast-radius startup
function and makes failure-path tests expensive. It was rejected for continued
development, while incremental extraction keeps upstream conflicts bounded.

### Split into microservices now

Separate services would add network contracts, distributed tracing, deployment
coordination, and more partial-failure modes before ownership and delivery
semantics are mature. It was rejected because the present scaling and team
boundary do not justify that cost.

### Run unrestricted multi-active replicas

The existing database lock lowers risk but is not a complete sharding and
fencing design. Starting multiple active session owners could corrupt operational
state or duplicate user-visible actions. It was rejected until ownership is
explicitly partitioned and fenced.

### Make every event transport durable immediately

This would silently change NATS and WebSocket contracts and could require new
storage and consumer protocols. It was rejected in favor of documenting current
guarantees and making later durability changes explicit.

## Consequences

- Startup wiring becomes testable in bounded modules without restructuring
  domain import paths.
- The system can improve recovery before taking on multi-active complexity.
- Active-passive capacity does not provide horizontal command throughput.
- Operators must treat PostgreSQL availability and ownership fencing as critical
  dependencies.
- Consumers must implement idempotency for at-least-once transports.
- Additional bootstrap extractions require separate focused pull requests.

## Rollout and replacement

Each extraction is deployed with the same configuration and image topology as
the preceding release. Validate startup, liveness, worker metrics, outbox age,
and a signed webhook/RabbitMQ canary before promotion. Roll back by restoring the
previous immutable image or reverting the focused extraction commit; no data
rewrite is required.

Replace active-passive only after an ADR defines partition ownership, fencing,
request routing, reassignment, duplicate suppression, and tested split-brain
recovery. Replace a transport guarantee only through a versioned contract and a
backward-compatible consumer rollout.
