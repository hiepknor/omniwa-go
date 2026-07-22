# ADR 0003: Labels projection storage

- Status: Accepted
- Date: 2026-07-22

## Context

The legacy `labels` table stores label definitions only. Label association
events are logged but not persisted, reads require a connected WhatsApp
client, and the records do not contain source versions needed to resolve
duplicate or out-of-order app-state events.

## Decision

Store the new read model in three versioned PostgreSQL tables:

- `projected_labels` for normalized label definitions.
- `projected_label_chat_associations` for chat assignments.
- `projected_label_message_associations` for message assignments.

Every record carries the source occurrence time and a deterministic event key.
An update is applied only when the tuple `(source_occurred_at,
source_event_key)` is newer than the stored tuple. Exact duplicate events and
older events therefore do not change the read model. Removal is represented by
a tombstone so a late add event cannot restore a deleted association.

Association tables intentionally do not reference `projected_labels`. WhatsApp
can deliver an association before its label definition, and ingestion must not
drop that valid out-of-order event. All three tables reference the instance and
are deleted with it.

This migration does not switch public endpoints or advertise the
`labels_projection` capability. Those changes require event ingestion and a
ready projection state first.

Initial population uses a guarded full sync of WhatsApp's `regular` app-state
collection. Full-sync app-state events are ingested into the durable inbox but
are not fanned out to legacy webhooks or WebSockets. A durable
`label_sync_complete` event sorts after mutation events and may mark the
projection ready only when the inbox contains no unprocessed label mutations.
This barrier prevents an empty or partially processed snapshot from enabling
the capability. The persisted ready state also prevents a full sync on every
reconnect.

The legacy `GET /label/list` path keeps its bare-array response and snake-case
fields, but reads the projection without requiring an active WhatsApp client.
Its `id` field is normalized to the stable provider label ID (the same value as
`label_id`) instead of exposing a database-generated UUID. New detail reads use
an additive envelope with projection freshness metadata. Reads return a
service-unavailable error until the projection is ready, so a valid empty
snapshot is never confused with an unfinished sync.

After WhatsApp confirms a label mutation, the service writes the normalized
change through to the projection. Definition write-through updates only the
mutable name, color, and deletion fields so it cannot erase metadata learned
from a full event. Echo events remain idempotent. A projection write failure
does not turn a confirmed WhatsApp mutation into an API failure; it marks the
projection stale and leaves the cached snapshot readable.

## Consequences

- Existing label behavior remains compatible while the projection is built.
- List and lookup indexes are scoped by instance and exclude tombstones.
- Projectors can process label definitions and associations independently.
- A later reconciliation step must handle an association whose definition
  never arrives.
