# ADR 0006: Durable normalized event history

## Status

Accepted

## Context

WhatsApp events are currently fanned out directly to WebSocket, webhook,
RabbitMQ, and NATS consumers. Those deliveries are transient: reconnecting a
WebSocket or restarting the server cannot recover prior events, and persisting
the raw provider or outbound payload would retain credentials, message content,
pairing material, and unstable provider-specific structures.

## Decision

Persist a normalized event envelope in PostgreSQL before starting any existing
best-effort fan-out. Each row has a durable UUID, instance, public event type,
occurrence time, ingestion time, expiry time, and a deliberately small JSON
summary. The summary allowlist contains identifiers and lifecycle metadata only;
it never contains raw provider payloads, API tokens, QR/passkey material,
message content, contact names, group names/topics, or media bytes.

A durable-write failure suppresses fan-out for that event. This preserves the
invariant that consumers are never notified of an event absent from durable
history. Existing webhook, WebSocket, RabbitMQ, and NATS payloads are otherwise
unchanged. This is not an exactly-once delivery guarantee: downstream delivery
remains best effort, and duplicate provider events may create distinct history
records until provider-specific deduplication keys are introduced.

`WA_EVENT_RETENTION` controls retention and defaults to `720h` (30 days).
Expired rows are hard-deleted by a bounded, `SKIP LOCKED` background sweep so
multiple replicas can clean safely without long blocking transactions.

No backfill is promised for events emitted before this persistence layer is
deployed. `GET /events` exposes instance-token-scoped history with an opaque
cursor ordered by `(occurred_at, id)`, an optional exact event-type filter, and
explicit retention and no-backfill metadata. `events_projection` is a server
capability rather than a sync-gated resource capability because a new
instance's empty history is immediately valid.

## Consequences

- Event history survives process restarts and does not depend on WebSocket
  connection state.
- Sensitive raw event data is not duplicated into the history store.
- A PostgreSQL write is now on the critical path before event fan-out; failures
  are logged with structured error codes and intentionally fail closed.
- Operators must choose retention appropriate to their legal basis and privacy
  obligations and should account for event-row volume in database sizing.
