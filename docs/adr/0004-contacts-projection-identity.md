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
and username aliases. Future ingestion must link or merge aliases inside a
database transaction before exposing the resulting contact.

## Consequences

- Contacts remain stable when new provider aliases are discovered.
- Duplicate and out-of-order events can be reconciled per field group.
- List and search endpoints can use normalized database columns without live
  WhatsApp queries.
- Alias merging requires transactional repository logic and explicit conflict
  tests before event ingestion is enabled.
