# ADR 0032: Stable canonical contact identity

## Status

Accepted and implemented. Permanent redirects use migration 34, local LID
reconciliation uses migration 35, and the additive public contact/chat contract
uses migration 36. Advertisement remains gated by
`WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED` and durable per-instance readiness.

## Context

WhatsApp may identify one person with a phone JID, a LID, or another current
JID. The Contacts projection already stores instance-scoped opaque contact IDs
and unique aliases, but an authoritative mapping that arrives late can merge
two contacts. The previous merge retained the lexicographically lowest UUID and
deleted the other row. A contact ID already returned to a client could therefore
stop resolving, and projected chats could retain the deleted ID.

Names and usernames are mutable and non-unique, so they cannot establish person
identity. Identity resolution must also remain local to the projection and must
never make a live WhatsApp request from a read path.

## Decision

Canonical contact IDs are opaque UUIDs scoped to one instance. Contacts merge
only when a normalized event or the local whatsmeow LID mapping store supplies
an authoritative shared alias. Display names, redacted phone values, and other
profile fields never cause a merge.

When more than one active contact resolves from an authoritative alias set, the
oldest `created_at` survives, with `contact_id` as a deterministic tie-breaker.
The merge transaction locks aliases in a stable order, combines independently
versioned fields, moves active aliases and projected-chat references, flattens
existing redirects, creates permanent redirects for absorbed IDs, and only then
deletes the absorbed contact rows. Replayed or out-of-order events pass through
the same idempotent transaction.

`projected_contact_redirects` is keyed by instance and absorbed contact ID. Its
canonical target is a live contact in the same instance and is protected by a
restricting foreign key. Repository reads by contact ID transparently follow a
single flattened redirect. Redirects are permanent: an absorbed ID is never
reused and remains a compatibility identifier for bookmarks and stored client
references.

The bounded reconciliation stage reads only
the local whatsmeow LID store; projection reads never call WhatsApp. It scans
canonical contacts in UUID order, persists a per-instance cursor, and uses a
time-bounded lease so another process can resume after a crash. Live contact
events and full local contact snapshots use the same resolver while the feature
gate is enabled. Missing mappings leave partial contacts unchanged; mapping
store failures release or expire the lease and are retried on a later bounded
run.

The public `ContactInfo` contract retains every legacy Pascal-cased field and
adds `contactId`, `addressingJid`, `aliases`, `identityStatus`, `displayName`,
`displayNameSource`, and `identityUpdatedAt`. Addressing prefers a phone JID,
then a LID, then the persisted preferred JID. `identityStatus=complete` means
both phone and LID identities are known; `partial` is not permission to merge by
name. `GET /user/contact/{contactId}` accepts a current UUID, a permanently
redirected absorbed UUID, or a contact JID alias, and always returns the current
canonical `contactId`.

Search applies NFKC normalization, whitespace folding, and case folding to
names and aliases. Its version-2 cursor is opaque and query/instance-bound;
version-1 cursors return `invalid_cursor` after migration. Pagination and totals
operate on active canonical contacts, never alias rows. Totals are exact at the
time of each request but are not a multi-page snapshot guarantee under
concurrent writes.

Direct projected chats link to the canonical contact locally and denormalize
its name with this precedence: full name, business name, push name, first name,
then username. Contact updates propagate to direct chats transactionally.
Groups, newsletters, and broadcasts retain their type-specific provider names.
Unknown contacts remain visible with absent `contactId`/`displayName` rather
than a synthesized phone-derived name.

`canonical_contact_identity` is advertised only when Contacts and Chats are
ready at their current schemas and the instance's version-1 LID reconciliation
checkpoint is complete. `contacts_projection` and `chats_projection` remain
independent lower-level capabilities.

## Alternatives

- Returning 404 for absorbed IDs was rejected because it contradicts stable
  identity and breaks durable client references.
- Time-limited redirects were rejected because clients have no safe point at
  which an opaque ID can be discarded.
- Selecting a survivor from profile completeness or display name was rejected
  because those values are mutable and can make replay order affect identity.
- Keeping both contacts and asking clients to merge them was rejected because
  aliases and mapping authority are backend-owned.

## Consequences and rollback

Redirect rows grow with real merges and require instance-scoped indexes, but
their records are small and preserve compatibility. Every future contact delete
or merge must respect the canonical-target foreign key and flatten redirects.
Cross-instance aliases and redirects remain impossible by database constraint
and query scope.

Migration 36 backfills direct-chat links/names and builds normalized expression
indexes with regular `CREATE INDEX` inside the migration transaction. Large
contact tables can therefore extend deployment time and briefly block
conflicting writes. Roll out during a maintenance window, observe migration
duration and locks on a production-sized copy first, and stop before application
promotion if the migration fails; the transaction rolls back atomically.

Rollback disables redirect-aware writes and reads while leaving migration 34 in
place. The additive table is harmless to older binaries. Merges performed after
deployment are not reversed automatically; aliases and chats already point to
the deterministic survivor, while the retained redirect records preserve the
only safe recovery path. Dropping redirects is explicitly outside rollback and
requires a separately approved destructive migration.

The LID reconciliation stage is rolled back by setting
`WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED=false` and restarting. Migration 35
and its checkpoints remain in place. Disabling the worker stops new mapping
merges but does not attempt to split contacts already joined by an authoritative
mapping; permanent redirects make that state backward compatible.

The public-contract behavior rollback is to stop advertising
`canonical_contact_identity` and deploy the previous binary while leaving
migration 36 in place. Its nullable chat columns and expression indexes are
backward compatible. Cursor version 2 must not be rolled back after clients have
started storing it; a rollback instead causes those cursors to fail closed as
`invalid_cursor`.
