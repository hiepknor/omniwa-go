# ADR 0007: Persisted overview metrics

## Status

Accepted

## Context

An operational overview must remain cheap to refresh and must not create
WhatsApp information queries. Combining lifetime entity counts with unspecified
message or event periods also produces misleading dashboards.

## Decision

`GET /server/overview` reads PostgreSQL only. An instance token sees its own
scope; an admin key sees server-wide aggregates. All counters are read in one
repeatable-read, read-only transaction and share one `generatedAt` timestamp.

The response distinguishes current active projection counts from windowed flow
counts. Groups, contacts, and chats exclude tombstones. Messages exclude
deleted rows and are counted by provider timestamp in the half-open interval
`[window.start, window.end)`, including incoming/outgoing breakdowns. Durable
events use the same interval. The default window is 24 hours and the maximum is
30 days (`720h`).

Instance connection counts use the persisted `instances.connected` flag. They
describe last recorded connection state, not an active WhatsApp probe. The
overview endpoint is metrics, not health; API liveness, connection state,
projection readiness, and query throttling will remain separate health
dimensions.

## Consequences

- Refreshing the overview never consumes the WhatsApp query budget.
- Every metric carries an explicit scope, window, and generation timestamp.
- Admin aggregation cost grows with projection table/index size and should be
  monitored; the 30-day bound prevents accidental unbounded flow scans.
- Persisted connection state may lag a sudden network failure until the normal
  connection event path updates it.
