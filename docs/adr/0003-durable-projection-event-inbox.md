# ADR 0003: Durable projection event inbox

- Status: Accepted
- Date: 2026-07-22

## Context

Projection updates must tolerate duplicate and out-of-order provider events,
process restarts, and multiple application replicas. Processing an event only
in the WhatsApp callback goroutine can lose work after a crash. A plain pending
row is also insufficient because two workers can process it concurrently or a
crashed worker can leave it permanently stuck.

## Decision

Normalize each projection-relevant event into the internal
`projection_event_inbox` table before projection processing. The stable
identity is `(instance_id, resource, event_key)`, so duplicate ingestion is a
no-op. `entity_key`, `event_type`, and `occurred_at` let each resource projector
apply its own ordering and tombstone rules.

Workers atomically claim bounded batches with `FOR UPDATE SKIP LOCKED`. A claim
has a random fencing token and an expiry. An expired claim is eligible for
recovery, while a stale worker cannot complete or fail work claimed by a newer
worker. Failed work records only a bounded error code and a retry time; raw
errors are not persisted.

Processing is at least once. Resource projectors must therefore use idempotent
upserts and compare provider occurrence/version data rather than treating inbox
arrival order as authoritative. Exactly-once external side effects are not
promised.

Payload is internal-only, limited to 1 MiB, and excluded from JSON
serialization. It must contain the minimum normalized data required by the
projector, not media binaries, credentials, or unrestricted provider payloads.

## Rollout and recovery

Migration 2 is additive and can be deployed before event producers or workers
are wired to it. Rolling the application back leaves an unused table in place.
On restart, workers claim pending, failed-and-due, and expired-processing rows.
Processed rows are retained until a separately defined retention job is
introduced; no cleanup policy is implied by this decision.

## Consequences

- Duplicate ingestion does not duplicate projection work.
- Multiple replicas can safely consume independent batches.
- Crashed work becomes resumable after its lease expires.
- Inbox persistence and projection mutation are not yet one transaction;
  projectors remain responsible for idempotency.
- Wiring producers, worker lifecycle, metrics, retention, and resource-specific
  projection logic are separate increments.
