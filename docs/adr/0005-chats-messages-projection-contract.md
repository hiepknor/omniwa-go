# ADR 0005: Chats and messages projection contract

## Status

Accepted

## Context

The legacy `messages` table stores only a globally unique provider message ID,
timestamp, status, source, and referral. It is not instance-scoped and cannot
support chat lists, stable message pagination, normalized content, media
metadata, delivery history, retention, or history-sync provenance.

Existing `/send/*`, `/message/*`, and `/chat/*` routes are action APIs and must
remain compatible. Read APIs therefore need additive paths backed exclusively
by persisted projections.

## Decision

Chats, messages, and per-recipient receipt transitions use separate
`projected_*` tables. They do not replace or migrate the legacy message table.
Every identity is instance-scoped. Messages are idempotent by instance and
provider message ID, and chats are idempotent by instance and chat JID.

Chat ordering uses `(lastActivityAt, chatId)`. Message history ordering uses
`(providerTimestamp, messageId)`. Both cursor components are required so a new
message cannot move or duplicate records already traversed by a client.
Receipts retain one ordered transition per message, recipient, and receipt type;
late and duplicate receipts will update through source-version comparison.

The normalized message model stores direction, sender/recipient/participant,
content text or summary, quoted-message identity, bounded media metadata,
lifecycle timestamps, provenance, and retention/deletion state. Media bytes and
raw provider payloads are not stored. `mediaObjectKey` may reference configured
object storage after media persistence is implemented.

The additive public contract will be:

- `GET /chat/list` for cursor-paged chat summaries.
- `GET /chat/info/{chatId}` for one projected chat.
- `GET /chat/{chatId}/messages` for cursor-paged message history.
- `GET /message/{messageId}/delivery` for ordered receipt history.

Filters, search, and cursors operate only on PostgreSQL. Existing action routes,
including every `/send/*` endpoint, retain their current request and response
contracts. `chats_projection` and `messages_projection` are enabled
independently only after their respective readiness barriers complete.

## Retention and privacy

`WA_MSG_RETENTION` defaults to `2160h` (90 days). Operators may shorten or
extend it according to their legal basis and user expectations. Expired rows
and internal normalized message events are deleted in bounded batches by an
auditable background job; deleting a message cascades its receipt history.
Logs must not include message content, media object keys, or participant
identifiers at info level.

## Consequences

- Stable list and history reads no longer require live WhatsApp queries.
- The legacy model and send actions remain available during incremental rollout.
- Content ingestion, receipt reconciliation, history sync, write-through, API
  reads, and retention are separate follow-up changes with independent gates.
- Schema indexes support cursor pagination and retention deletion from the first
  release, avoiding a later blocking table rewrite.
