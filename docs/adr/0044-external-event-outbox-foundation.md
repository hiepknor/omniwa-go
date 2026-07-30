# ADR 0044: External event outbox foundation

- Status: Accepted
- Date: 2026-07-30

## Context

OmniWA GO writes a normalized row to `durable_events` before external fan-out,
but Webhook, RabbitMQ, and NATS delivery starts from callback-owned goroutines.
The webhook adapter acknowledges admission to an in-memory queue rather than a
successful HTTP response. A process crash, queue saturation, broker outage, or
shutdown can therefore leave durable history claiming an event existed while
the corresponding external delivery is lost.

WebSocket has different semantics. A live socket has no durable downstream
acknowledgement and clients already reconnect and resynchronize through REST.
Replaying old frames into a newly connected socket would blur realtime and
history contracts without proving consumption.

External compatibility payloads may contain message content. Retrying the same
payload after restart requires temporary persistence, which expands the
at-rest privacy surface beyond the deliberately minimal durable-event summary.
The record must therefore be internal-only, credential-sanitized, size-bounded,
retention-bound, and never logged or exposed by an API.

This is an L3 distributed-state and database decision. The schema foundation
must land independently from the traffic switch so the previous image remains
deployable throughout rollout.

## Decision

Add the forward-only `external_event_outbox` table in migration 39. One row is
one logical delivery to Webhook, RabbitMQ, or NATS and links to the normalized
durable event. Instance and durable-event deletion cascade to outbox rows,
making instance deletion authoritative and bounding outbox lifetime by the
existing durable-event retention policy.

The state machine is `pending -> processing -> delivered|dead_letter`, with a
return from `processing` to `pending` for a scheduled retry. Workers will claim
bounded batches with `FOR UPDATE SKIP LOCKED`, a random fencing token, and an
expiring lease. A superseded worker cannot acknowledge, retry, or dead-letter a
claim. Retry policy version and attempt ceiling are snapshotted per row so a
deployment does not silently change the policy of accepted work.

Persist the durable-history row and all selected delivery rows in one database
transaction. The persistence boundary recursively removes credential-shaped
fields and rejects malformed, scalar, or payloads larger than one MiB. Payload
fields use `json:"-"`; no public endpoint, capability, or OpenAPI operation is
added by this foundation. Successful acknowledgement replaces the compatibility
payload with an empty JSON object immediately; pending and dead-letter rows
retain it only while replay remains possible.

WebSocket remains direct and best-effort. The integration increment will add a
supervisor-owned worker and transport adapters, delivery identifiers for
consumer deduplication, bounded classified retry, metrics, and an operational
dead-letter policy. The current fan-out path remains authoritative until that
increment is explicitly enabled and observed.

## Alternatives

### Keep durable history and document best-effort delivery

This is operationally simple but preserves the known crash window and makes
the history/delivery invariant misleading. It was rejected.

### Use broker durability only

RabbitMQ publisher confirms can prove broker admission, but they do not cover
webhooks, NATS Core, or the database-to-broker crash window. It was rejected as
the common reliability boundary.

### Replay every transport, including WebSocket

WebSocket clients cannot durably acknowledge a frame. Treating a successful
socket write as consumption would still lose events and surprise clients with
historical frames. It was rejected; `/events` remains the recovery contract.

### Persist only the normalized durable-event summary

The summary intentionally omits message content and other compatibility data,
so it cannot recreate existing external payloads. Changing all consumers to a
new minimal envelope is valuable but is a separate versioned-contract rollout.

## Consequences

- Webhook and broker work can become restart-safe and at-least-once after the
  integration increment switches traffic.
- Consumers must deduplicate by the stable delivery identifier once it is added
  to transport metadata; exactly-once external side effects are not promised.
- Compatibility payloads are temporarily stored in PostgreSQL. Database access,
  encryption-at-rest, backup retention, and durable-event retention therefore
  govern message-content exposure.
- Outbox saturation, oldest pending age, retries, and dead letters become
  mandatory operational signals before serve mode is enabled.
- Migration 39 is additive and does not change current event delivery behavior.

## Rollout and rollback

1. Deploy migration 39 and the unused repository foundation.
2. Verify empty/existing/repeated/concurrent migration startup and repository
   transaction, lease recovery, fencing, retry, dead-letter, and isolation.
3. Add the worker and adapters behind a default-off serve flag in a later PR.
4. Canary selected development instances, compare direct and outbox routing,
   then switch one transport at a time.
5. Remove direct durable transports only after queue depth and delivery-error
   thresholds remain clear for an agreed observation window.

Application rollback redeploys the prior image and leaves the empty or unused
additive table in place. If a later integration rollback is required, disable
serve mode before deploying the previous image; retained rows remain available
for a forward fix. Migration history is never edited, and destructive cleanup
requires a separate post-observation migration.
