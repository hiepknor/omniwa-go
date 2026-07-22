# ADR 0004: Contacts projection identity and field ordering

## Status

Accepted

## Context

WhatsApp may identify the same person by a phone-number JID, a LID, or a
username. Those identifiers can arrive at different times. Contact, push-name,
picture, and user-about events also update independent fields and may be
duplicated or delivered out of order.

Using a provider identifier as the database primary key would make later
identity linking destructive. Using one source timestamp for every contact
field would incorrectly discard a valid older event for one field after an
unrelated field received a newer event.

## Decision

Each projected contact has an instance-scoped UUID identity. Provider
identifiers are stored in a separate alias table and resolve to that stable
identity. The API-facing preferred JID remains explicit on the contact record,
so existing contracts do not need to expose the internal UUID.

Contact records store normalized fields only. Provider event payloads are not
part of the public read model. A JSON field-version map records independent
ordering for contact details, push name, business name, picture, and user-about
updates. The latest entity-level source version remains available for freshness
and diagnostics, but it must not be used to reject an update to an unrelated
field group.

The identity table is instance-scoped and distinguishes JID, phone JID, LID,
and username aliases. The repository serializes alias resolution with sorted
transaction-scoped advisory locks. When a later event proves that aliases from
separate records belong together, it merges their independently versioned
fields, moves every alias, and removes the duplicate inside one transaction.

Contact, push-name, business-name, picture, and user-about events are normalized
into the durable projection inbox before best-effort webhook and queue fan-out.
A resource-specific worker applies them to the repository and records projection
state only after the database write succeeds. Group picture events are excluded.
The worker does not mark the projection ready or advertise the capability;
initial synchronization must establish that boundary separately.

## Consequences

- Contacts remain stable when new provider aliases are discovered.
- Duplicate and out-of-order events can be reconciled per field group.
- List and search endpoints can use normalized database columns without live
  WhatsApp queries.
- Duplicate concurrent writes for the same alias resolve to one contact; the
  PostgreSQL integration suite verifies this invariant.
