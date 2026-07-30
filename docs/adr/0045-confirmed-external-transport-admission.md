# ADR 0045: Confirmed external transport admission

- Status: Accepted
- Date: 2026-07-30

## Context

ADR 0044 added the persistence foundation for an external event outbox. The
existing producer interface cannot be used as its delivery acknowledgement:
Webhook returns after admission to an in-memory queue, RabbitMQ enabled confirm
mode without waiting for a broker confirmation, and NATS Core returned after a
client-side publish without flushing. A missing NATS connection was also
reported as success.

Marking an outbox row delivered at any of those boundaries would recreate the
loss window the outbox is intended to close.

## Decision

Establish explicit confirmed-admission primitives before integrating a worker:

- Webhook synchronous delivery succeeds only on an HTTP 2xx response and
  returns a bounded classification containing retryability and status, never
  the destination URL or response body.
- RabbitMQ delivery succeeds only after a positive publisher confirmation.
  Publish errors, negative acknowledgements, closed confirmation streams, and
  confirmation timeouts are failures.
- NATS Core delivery succeeds only after `FlushTimeout` and `LastError` checks.
  An unavailable configured connection is a failure.

NATS Core flush proves server admission only. It does not provide persistence
or durable consumer acknowledgement, so NATS must not be described as a
durable transport. NATS outbox traffic remains disabled until a later decision
either adopts JetStream or explicitly accepts server-admission semantics.

The legacy producer interface and current direct fan-out remain in place in
this increment. No outbox row is served and no public API changes.

## Consequences

- A later outbox worker can acknowledge Webhook and RabbitMQ work at an
  authoritative transport boundary.
- RabbitMQ may emit duplicates after an ambiguous confirmation timeout; this is
  required by at-least-once delivery and consumers must deduplicate.
- Core NATS is deliberately excluded from a durability claim.
- Transport errors become observable to callers instead of false success.

## Rollout and rollback

Deploy this prerequisite without enabling outbox serve mode and observe direct
producer errors. Application rollback restores the previous image; there is no
schema or data rollback. The next increment adds worker ownership, telemetry,
and default-off routing only after these boundaries are exercised in tests.
