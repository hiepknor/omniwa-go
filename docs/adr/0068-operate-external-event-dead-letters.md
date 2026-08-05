# ADR 0068: Operate external event dead letters without exposing payloads

- Status: Accepted
- Date: 2026-08-05

## Context

The durable external-event outbox recovers expired leases, retries bounded
failures, and terminally dead-letters exhausted or permanent failures. Metrics
and aggregate health reveal that dead letters exist, but operators cannot safely
inspect or replay an individual delivery. Direct database edits risk leaking
message payloads, bypassing claim fencing, or replaying a delivered event.

## Decision

Add admin-only list and replay endpoints under
`/server/external-event-failures`. List responses contain bounded delivery
metadata but never payloads, routing keys, durable-event IDs, claim tokens, or
lease tokens. Pagination cursors are opaque and bound to their filters.

Replay is a conditional transaction that only changes a `dead_letter` row. It
resets the attempt and claim state, retains the internal sanitized payload and
route, and inserts an audit row containing the reason, request ID, timestamp,
and a domain-separated hash of the admin credential. A missing row returns 404;
a row that is no longer dead-lettered returns 409.

## Rollout and rollback

Run migration 42 before serving the new endpoint, deploy one replica, and first
exercise list-only access. Replay one known non-production consumer event and
verify the pending, attempt, delivered, and dead-letter metrics. Restrict the
endpoint to the existing global admin credential and retain API access logs.

Rollback the application while leaving the additive audit table in place. The
old binary ignores it, so no destructive down migration is required. Never drop
the table during an incident because it is the replay audit trail.

## Consequences

Operators gain a bounded recovery path without database mutation or payload
exposure. Delivery remains at-least-once: consumers must be idempotent because a
previous transport success may have occurred before its acknowledgement failed.
