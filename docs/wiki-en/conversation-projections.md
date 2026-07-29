# Conversation projections

Conversation list reads are served from instance-scoped PostgreSQL projections.
They never call WhatsApp, and chat list reads do not perform per-row contact
lookups.

## Canonical contacts

`ContactInfo` preserves the existing Pascal-cased fields and adds:

- `contactId` (required): stable opaque UUID within the authenticated instance.
- `addressingJid` (required): phone JID when known, otherwise LID, otherwise the
  persisted preferred JID. Use this value for commands.
- `aliases` (required): deduplicated known active JID, phone-JID, LID, and
  username alias values. Clients should treat the array as lookup material, not
  additional contacts.
- `identityStatus` (required): `complete` when phone and LID are both known,
  otherwise `partial`.
- `identityUpdatedAt` (required): last canonical projection update.
- `displayName` and `displayNameSource` (optional): present only when a real
  projected name is known.

Contacts merge only on authoritative aliases supplied by normalized events or
the local whatsmeow LID mapping store. Names never merge contacts. IDs are
instance scoped, and an absorbed ID permanently redirects to its canonical
contact. `GET /user/contact/{contactId}` accepts a current UUID, an absorbed
UUID, or a contact JID alias and always returns the canonical ID.

Search applies Unicode NFKC normalization, collapses whitespace, and compares
case-insensitively across canonical/absorbed IDs, aliases, username, and names.
Pagination is over canonical contacts. Version-2 cursors are opaque and bound
to the instance and normalized query; old or mismatched cursors return HTTP 400
with `invalid_cursor`.

## Chat names

Direct chats expose canonical `contactId` when the mapping is known and copy the
contact name into `displayName`. Precedence is `FullName`, `BusinessName`,
`PushName`, `FirstName`, then `Username`; `displayNameSource` reports the chosen
source and `displayNameUpdatedAt` reports its projection timestamp. A contact
rename updates linked direct chats transactionally.

Group, newsletter, and broadcast names use `group_subject`, `newsletter_name`,
and `broadcast_name` respectively. Direct provider names use `provider_chat`
only while no canonical name is available. A chat remains present when contact
identity or name is unknown. The backend never synthesizes a display name from
a phone number or other sensitive identifier.

## Totals and readiness

`GET /chat/list` returns the exact active projected-chat count in `meta.total`.
`GET /user/contacts` returns the exact active canonical-contact count, and
`GET /user/contacts/search` returns the exact canonical count matching the
normalized query. Totals are recomputed for each request and remain stable
across pages when the dataset is unchanged; they are not snapshot-isolated from
concurrent writes. Search does not provide `unfilteredTotal`.

A ready empty projection returns `total: 0`. A projection that is not started,
failed, or has never completed reconciliation returns `projection_not_ready`
instead of a false empty result. A previously reconciled syncing or stale
projection may be served with its explicit `syncStatus`.

The `canonical_contact_identity` capability appears only when Contacts and
Chats are ready at their current schema versions and the instance's local LID
reconciliation checkpoint is complete. Clients must not infer canonical
identity support from `contacts_projection` alone.

## Canonical conversation shadow rollout

Migration 37 adds an internal canonical-conversation identity, provider-chat
alias mapping, redirects, nullable Chat/Message associations, and a resumable
backfill checkpoint. Projection writes maintain these rows transactionally,
but the foundation does not change the public Chat DTO, legacy list total, or
legacy cursor scope.

Only direct Chats that already reference the same canonical Contact may share
the shadow conversation. Partial direct identities remain isolated. Group,
newsletter, broadcast, and unknown Chats remain isolated by type and provider
chat ID. No name, phone-text, timestamp, or content heuristic participates in
the mapping.

The shadow aggregate marks unread state non-authoritative. Operators and
clients must not expose it, group legacy Chat rows, or infer canonical Chat
support from the presence of migration 37. The follow-up backfill/readiness
rollout will advertise a separate `canonical_chat_identity` capability only
after all aliases and retained messages are associated, unread is
authoritative, redirects validate, and Contacts/Chats/Messages are ready.

Rollback before that capability is advertised uses the previous binary and
leaves the additive nullable columns/tables unused. Do not drop the shadow
schema while any new binary may write it.

## Projected message field contract

Every message returned by `GET /chat/{chatId}/messages` and
`GET /message/{messageId}` has these required fields: `messageId`, `chatId`,
`direction`, `messageType`, `providerTimestamp`, and `provenance`. The live,
history-sync, and synthetic historical ingestion paths all validate or provide
these values before a row can enter the projection.

Sender, recipient, participant, normalized content, media metadata, receipt
timestamps, `historySyncId`, `mediaAssetId`, and `retentionExpiresAt` remain
optional because message type, direction, provider availability, or historical
source can legitimately omit them. For display ordering, clients use
`providerTimestamp`, then `sentAt`, then `deliveredAt`; if none is reported by
an older unsupported record, display an unreported timestamp rather than
inventing one. Current-schema responses always include `providerTimestamp`.

`mediaAssetId` is an opaque reference to shared private media, not proof that
bytes are ready. The projected message remains authoritative when media is
downloading, processing, failed, expired, or deleted. Fetch asset metadata to
learn the lifecycle state and authenticated content only when it is ready.
Binary bytes, provider descriptors, object keys, and private storage URLs are
never part of the message projection.
