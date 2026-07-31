# ADR 0048: Retire direct Webhook and RabbitMQ adapters

- Status: Accepted
- Date: 2026-07-31

## Context

ADRs 0044 through 0047 introduced atomic external-event recording, confirmed
transport delivery, and a staged dual-path cutover. A real development cohort
validated Webhook and RabbitMQ delivery through both paths with no duplicates,
invalid delivery identifiers, failed rows, or compatibility dispatches. Keeping
the direct adapters now adds two retry owners, duplicate subscription mapping,
runtime flags with unsafe combinations, and a permanent risk that the paths
diverge.

## Decision

Webhook and RabbitMQ routes are always selected by the application emitter and
atomically stored with durable event history. The outbox worker always runs and
is their only delivery owner. PostgreSQL owns admission, leases, retry state,
and recovery. Webhook uses confirmed HTTP responses and RabbitMQ uses publisher
confirms. Both carry a stable delivery ID for consumer idempotency.

NATS and WebSocket remain direct realtime transports. They run only after the
atomic history/outbox transaction succeeds. Subscription and global-event
selection stay in the shared emission package; transport code does not recreate
those decisions.

The rollout flags `EXTERNAL_EVENT_OUTBOX_SERVE_ENABLED` and
`EXTERNAL_EVENT_OUTBOX_EMIT_TRANSPORTS`, the direct-adapter compatibility
metric, the Webhook in-memory queue, and RabbitMQ direct retry loop are removed.
The existing outbox worker tuning variables remain. No schema or data migration
is required.

## Alternatives considered

- Retain dual paths indefinitely: rejected because it doubles operational state
  and permits silent semantic drift.
- Revert to direct delivery: rejected because accepted Webhook work can be lost
  on restart and RabbitMQ retry state is not durable.
- Make NATS and WebSocket durable in the same change: deferred because their
  realtime connection semantics and acknowledgement model require a separate
  design.

## Consequences

Webhook and RabbitMQ delivery is at least once and can be delayed during a
database or target outage. Operators must monitor outbox pending, processing,
dead-letter, oldest-age, and attempt metrics. Delivery-ID-aware consumers can
deduplicate ambiguous acknowledgements. Removing rollout flags is an intentional
configuration break; stale variables are ignored by the new binary.

## Rollout and rollback

Deploy one immutable image and verify liveness, revision identity, outbox
health, stable delivery IDs, confirmed Webhook responses, and confirmed
RabbitMQ publishes. Roll back only after stopping new emission and draining
pending and processing rows. The previous image must run with its outbox worker
enabled and both Webhook and RabbitMQ selected for durable emission; otherwise
it may duplicate or strand work. Dead-letter rows remain operator-visible and
must not be silently discarded.
