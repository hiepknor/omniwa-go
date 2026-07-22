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
Mutation ingestion alone does not mark the projection ready or advertise the
capability; initial synchronization establishes that boundary separately.

Initial synchronization reads the local whatsmeow contact store after the
regular app-state sync is confirmed. On reconnect, an already populated local
store may seed the projection immediately; an empty unconfirmed store is
deferred so it cannot be mistaken for a valid empty address book. The snapshot
uses the same repository field groups, then enqueues a durable completion
barrier. The worker marks Contacts ready only when no normalized contact
mutation remains unprocessed. Only that ready state at the current schema
version enables `contacts_projection`.

`GET /user/contacts` keeps its existing message/data envelope and legacy
Pascal-cased contact fields, but reads exclusively from the projection and adds
freshness metadata. A valid ready-empty projection returns an empty JSON array;
an unreconciled projection returns HTTP 503. The additive
`GET /user/contact/{contactId}` endpoint resolves the JID returned by the list
and exposes the same normalized model. Optional phone/LID identity, username,
redacted phone, picture, and user-about fields are additive; raw provider event
payloads remain internal.

The additive `GET /user/contacts/search` endpoint provides case-insensitive
prefix search over normalized identity and display fields. It uses bounded
keyset pagination ordered by normalized preferred JID and contact UUID. Cursors
are versioned and bound to the normalized query, so a cursor cannot silently be
reused with a different search. Search is instance-scoped, treats SQL wildcard
characters literally, and never queries WhatsApp. Migration 13 adds partial
functional indexes for the search and ordering expressions.

## Consequences

- Contacts remain stable when new provider aliases are discovered.
- Duplicate and out-of-order events can be reconciled per field group.
- List and search endpoints can use normalized database columns without live
  WhatsApp queries.
- Duplicate concurrent writes for the same alias resolve to one contact; the
  PostgreSQL integration suite verifies this invariant.
