# ADR 0035: Canonical conversation identity foundation

## Status

Accepted.

## Context

WhatsApp can address one direct correspondent through a phone-number JID and a
LID. Contact reconciliation can prove that those aliases belong to one person,
but the Chat projection remains keyed by the provider `chat_id`. History sync
may therefore create a LID chat while live events create a phone-JID chat for
the same canonical Contact.

Clients cannot safely combine those rows. Each row currently owns independent
message pagination, unread snapshots, last activity, and list totals. Display
name and phone-text matching are not identity evidence, and moving aggregation
rules into a frontend would make totals and cursors non-authoritative.

Changing the existing public `chatId` value to an opaque identifier would also
break clients that use it as a provider recipient or path parameter during a
mixed rollout.

## Decision

Introduce a separate, opaque, instance-scoped `conversation_id`. Provider chat
IDs remain durable aliases and are retained on Chats and Messages for
compatibility and provenance.

The storage foundation consists of:

- `projected_conversations`, the canonical identity and aggregate shadow row;
- `projected_chat_aliases`, mapping every provider chat ID to one conversation;
- `projected_conversation_redirects`, preserving absorbed opaque IDs;
- nullable `conversation_id` columns on existing Chats and Messages; and
- `projected_conversation_backfills`, a leased, resumable per-instance
  checkpoint with bounded counters.

Direct chats may share a conversation only when they reference the same
canonical Contact. A deterministic UUID is derived from the instance UUID and
canonical Contact UUID. A direct chat without a canonical Contact remains
isolated by provider chat ID. Groups, newsletters, broadcasts, and unknown
types are always isolated by type and provider chat ID. Display names, phone
text, message content, and timestamps never participate in identity.

Projection writes create or update the shadow association transactionally.
Message association resolves the durable chat-alias mapping; it does not call
WhatsApp. Contact merges re-associate all affected direct aliases in the same
database transaction, flatten redirects, and clear absorbed Contact references
before the absorbed Contact is deleted.

The initial shadow unread aggregate is explicitly marked non-authoritative.
The canonical public capability and serving mode remain disabled until a later
backfill establishes an authoritative unread invariant, associates all retained
messages, validates redirects, and confirms projection readiness.

## Identity and concurrency invariants

- Every primary key and lookup includes `instance_id`.
- One active direct conversation may reference a canonical Contact per
  instance; a partial unique index enforces this invariant.
- One provider chat alias maps to exactly one conversation per instance.
- Conversation identity writes take a transaction-scoped advisory lock over
  the instance and logical identity, preventing concurrent PN/LID creation.
- Message identity remains `(instance_id, message_id)`; no content- or
  timestamp-based deduplication is introduced.
- Redirects never cross instances and are flattened when an intermediate
  conversation is absorbed.
- Existing public Chat reads, totals, and cursors continue to use legacy rows
  while the canonical capability is absent.

## Alternatives

### Group existing Chat DTOs in the frontend

Rejected. The frontend cannot authoritatively aggregate messages, unread
state, receipts, pagination, redirects, or totals.

### Reuse the surviving provider chat ID as canonical identity

Rejected. Addressing preference can change as PN/LID mappings arrive, making a
provider JID an unstable canonical identifier and coupling identity to command
routing.

### Replace `chatId` immediately

Rejected. It is a semantic breaking change even if the JSON field name remains
the same. An additive opaque identifier supports mixed deployments.

## Rollout and rollback

1. Apply migration 37 while canonical serving remains disabled.
2. Deploy transactional shadow dual-writes and monitor conflicts.
3. Run the resumable per-instance backfill and validation introduced by the
   follow-up change.
4. Advertise `canonical_chat_identity` only after Contact, Chat, and Message
   readiness plus backfill validation are all complete.
5. Enable additive canonical reads; retain legacy identifiers and aliases.

Rollback before capability enablement uses the previous binary and leaves the
additive nullable columns and shadow tables unused. Do not drop them while any
new binary may still write them. After capability enablement, first disable the
capability/serving mode, then roll back the binary. Destructive schema cleanup
is deferred to a separate, explicitly approved migration.
