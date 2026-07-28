# ADR 0032: Stable canonical contact identity

## Status

Accepted as an additive foundation. Public canonical-contact fields, local LID
reconciliation, backfill, and capability advertisement are separate rollout
stages.

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

The schema migration is expand-only and does not change the public HTTP
contract or advertise a capability. A later bounded reconciliation stage may
read the local whatsmeow LID store, but projection reads never call WhatsApp.

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

Rollback disables redirect-aware writes and reads while leaving migration 34 in
place. The additive table is harmless to older binaries. Merges performed after
deployment are not reversed automatically; aliases and chats already point to
the deterministic survivor, while the retained redirect records preserve the
only safe recovery path. Dropping redirects is explicitly outside rollback and
requires a separately approved destructive migration.
