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

The bounded local-mapping pass is resumable and is reopened on every successful
connection. A HistorySync carrying PN/LID mappings also triggers a serialized
refresh after its projection events are durably ingested. This closes the race
where the connection pass completes before Whatsmeow persists a newly learned
mapping. Resolution reads the parameterized local SQL mapping table directly
instead of the live client's in-memory cache, so a prior negative cache entry
cannot mask persisted authority. During an incomplete or failed refresh,
canonical identity readiness fails closed rather than presenting partial
reconciliation as ready.

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

`GET /conversations` returns the exact active canonical-conversation count in
`meta.total`.
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

## Canonical conversations

Migration 37 adds canonical-conversation identity, provider-chat alias mapping,
redirects, nullable Chat/Message associations, and a resumable backfill
checkpoint. Migration 38 adds message-level unread state and the fail-closed
snapshot evidence needed to serve the aggregate publicly.

Only direct Chats that already reference the same canonical Contact may share
the shadow conversation. Partial direct identities remain isolated. Group,
newsletter, broadcast, and unknown Chats remain isolated by type and provider
chat ID. No name, phone-text, timestamp, or content heuristic participates in
the mapping.

Provider unread snapshots are converted to message-level state only when at
least the reported number of incoming messages remains in the projection. The
newest N messages are selected deterministically by `providerTimestamp` and
`messageId`. Live incoming messages start unread, outgoing messages start read,
and incoming read-self receipts plus successful local mark-read commands update
the same rows idempotently. Canonical unread is a count of distinct projected
message IDs, never a sum or maximum of alias Chat counters. Insufficient history
keeps the aggregate non-authoritative. Retention of an unread message also
invalidates readiness instead of silently lowering the total.

Provider HistorySync may also contain metadata-only `WebMessageInfo` stubs with
no message payload. They do not represent projected messages and are excluded
before parsing. A payload-bearing record that lacks a required message identity,
chat, timestamp, or valid durable event still fails the sync and suppresses the
completion barrier; the backend never converts malformed message data into a
ready projection.

Set `WA_CANONICAL_CHAT_IDENTITY_ENABLED=true` only after
`WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED=true`. The worker scans active
projected Chats in opaque provider-ID order, uses a two-minute lease, persists
its cursor and counters, and can resume after a process restart. Live writes
use the same entity locks and association transaction, so a Chat created below
the persisted cursor is still associated without being rescanned. Batch size
and bounded work per connection cycle are controlled by
`CONVERSATION_BACKFILL_BATCH` and `CONVERSATION_BACKFILL_MAX_BATCHES`.

Structural completion validates every active Chat alias and retained Message,
redirect flattening, active-conversation ownership, and direct Contact
agreement. `canonical_conversation_identity` is advertised per instance only when
Contacts, Chats, and Messages are ready at their current schemas, both Contact
and conversation checkpoints are complete, structural validation succeeds,
and every active canonical conversation has authoritative unread state.

Do not add or maximize unread counts from PN/LID alias rows: either operation
can double-count or discard unread messages. Capability absence is the
machine-readable readiness signal; clients must not treat it as an empty result.

Rollback disables `WA_CANONICAL_CHAT_IDENTITY_ENABLED` and restarts the binary.
This removes the capability without deleting the additive schema. Restoring the
removed Chat reads requires redeploying a pre-removal binary; see ADR 0039.

### Canonical Conversation API

The preferred public read contract is:

- `GET /conversations`
- `GET /conversations/{conversationRef}`
- `GET /conversations/{conversationRef}/messages`
- `GET /conversations/{conversationRef}/messages/{messageId}`

Use these endpoints only when `canonical_conversation_identity` is advertised.
Clients must not infer the capability from the server version.

`conversationRef` accepts a current canonical UUID, an absorbed Conversation
UUID, or a current/absorbed provider Chat ID alias. Responses always contain the
current canonical `conversationId`. `ProjectedConversation` uses `aliases` for
provider lookup material and keeps `addressingJid` separate as the command
target. The message response requires `conversationId` and exposes the arrival
alias only as optional `providerChatId` provenance.

Conversation list totals count canonical rows. Message history aggregates every
authoritative alias and deduplicates by the instance-scoped provider message
key. Version-2 cursors remain opaque and canonical-conversation scoped; legacy
or cross-conversation cursors return `invalid_cursor`.

Canonical Conversation commands are published independently behind
`conversation_app_state_commands` and `conversation_history_sync`:

- `POST` and `DELETE /conversations/{conversationRef}/archive`
- `POST` and `DELETE /conversations/{conversationRef}/pin`
- `PUT` and `DELETE /conversations/{conversationRef}/mute`
- `POST /conversations/{conversationRef}/history-sync`

Archive, pin, and finite mute resolve the authoritative `addressingJid` after
resolving the canonical or absorbed reference. History sync accepts a projected
anchor message and derives the provider Chat alias from that message, which may
differ from `addressingJid` for PN/LID direct Conversations. Direct and group
Conversations are supported; newsletter, broadcast, unknown, and infinite mute
semantics fail closed. A provider acknowledgement returns HTTP 202 and the
projection is updated only from subsequent authoritative app-state events.

The legacy provider commands remain under `/chat/*` during the measured
compatibility window. They accept provider metadata and must not be given a
canonical UUID.

### Removed Chat-read contract

ADR 0039 physically removed `GET /chat/list`, `GET /chat/info/{chatId}`,
`GET /chat/{chatId}/messages`, and `GET /message/{messageId}` together with the
legacy DTOs and capability alias. Provider commands under `/chat/*` and
`GET /message/{messageId}/delivery` remain. A removed path now receives the
router's normal not-found response; there is no runtime compatibility flag or
HTTP 410 tombstone in the current binary.

Prometheus exposes canonical `omniwa_conversation_api_requests_total` and
`omniwa_conversation_api_request_duration_seconds` series with bounded labels.
Historical `contract="legacy_chat"` series may remain in durable storage, but
the current binary does not emit them.

## Projected message field contract

Every canonical message returned under `/conversations/{conversationRef}` has
required `messageId`, `conversationId`, `direction`, `messageType`,
`providerTimestamp`, and `provenance` fields. The live, history-sync, and
synthetic historical ingestion paths validate or provide these values before a
row can enter the projection.

Sender, recipient, participant, normalized content, media metadata, receipt
timestamps, `historySyncId`, `mediaAssetId`, and `retentionExpiresAt` remain
optional because message type, direction, provider availability, or historical
source can legitimately omit them. For display ordering, clients use
`providerTimestamp`, then `sentAt`, then `deliveredAt`; if none is reported by
an older unsupported record, display an unreported timestamp rather than
inventing one. Current-schema responses always include `providerTimestamp`.

`providerChatId` is optional provenance for the alias on which the message
arrived. It is not an entity identity; clients use `conversationId` for
navigation and cursor scope.

`mediaAssetId` is an opaque reference to shared private media, not proof that
bytes are ready. The projected message remains authoritative when media is
downloading, processing, failed, expired, or deleted. Fetch asset metadata to
learn the lifecycle state and authenticated content only when it is ready.
Binary bytes, provider descriptors, object keys, and private storage URLs are
never part of the message projection.
