# ADR 0038: Additive canonical Conversation public contract

> The combined identity/unread readiness predicate in this ADR is superseded
> by ADR 0049. The public Conversation terminology and contract remain active.

- Status: Accepted
- Date: 2026-07-29

> Superseded in part by ADR 0039, which removes the deprecated Chat read
> compatibility contract after the canonical Conversation cutover.

## Context

The canonical identity work in ADRs 0035 through 0037 made a Conversation the
backend-owned aggregate for Chat aliases and Messages. Public reads nevertheless
remained under `/chat/*` and returned `ProjectedChat`, `ChatType`, and the
required provider-facing `chatId`. This was intentionally compatible during the
identity rollout, but it now conflicts with the product domain, whose entity is
Conversation and whose stable identity is `conversationId`.

This is a public-contract L3 change. Existing clients may store `/chat/*` URLs,
version-1 or version-2 cursors, `ProjectedChat.chatId`, provider JIDs, or the
`canonical_chat_identity` capability. The migration must not change those
values, rewrite historical migrations, infer support from a version string, or
make a frontend responsible for identity reconciliation.

## Investigation and terminology inventory

The audit traced routes through handlers, the application reader, PostgreSQL
repositories, projection models, migrations, capability evaluation, generated
OpenAPI, tests, and operator documentation.

### A. Public product/domain contract

- Canonical `conversationId`, Conversation list/detail/history semantics,
  canonical totals and cursor scope.
- Contact-owned `addressingJid`, which is a command target rather than entity
  identity.
- The new `/conversations` reads and `ProjectedConversation` /
  `ProjectedConversationMessage` schemas.

### B. Public legacy compatibility contract

- `GET /chat/list`, `GET /chat/info/{chatId}`, and
  `GET /chat/{chatId}/messages`.
- `ProjectedChat`, `ProjectedMessage.chatId`, `chatAliases`, `ChatType`, and
  `canonical_chat_identity`.
- Existing `/chat/*` command request bodies and their provider-JID `chat`
  field.

### C. Internal canonical domain

- `projected_conversations`, conversation redirects, deterministic
  instance-scoped conversation UUIDs, Contact-backed direct-conversation
  reconciliation, aggregate unread, and canonical list/message cursors.
- `ChatMessageReader` canonical branches and `ConversationRecord` already own
  authoritative list/detail/history behavior.

### D. WhatsApp/provider adapter terminology

- whatsmeow `types.MessageInfo.Chat`, `types.JID`, provider Chat IDs, app-state
  archive/pin/mute builders, HistorySync targets, and message `chat_id`
  provenance.
- PN and LID provider aliases. These names are accurate and are not renamed.

### E. Database, persistence, and migration implementation

- Historical migrations 37 and 38, `projected_chats.chat_id`,
  `projected_messages.chat_id`, `projected_chat_aliases`, nullable association
  columns, redirect rows, and backfill checkpoints.
- These structures already represent the required canonical model. No schema
  or data migration is needed for this API-only expansion.

### F. Documentation, tests, and generated OpenAPI

- ADRs 0005 and 0032 through 0037, the Conversation projection guide, the
  single-replica rollout runbook, handler annotations, `docs/swagger.json`,
  `docs/swagger.yaml`, `docs/docs.go`, projection reader/repository tests, and
  capability tests.

## Findings

`ProjectedChat` has two modes. Without canonical readiness it is a provider Chat
projection. With `canonical_chat_identity` it is an adapter over
`projected_conversations`; in that mode it is semantically a projected
Conversation carrying legacy fields.

`chatId` and `conversationId` are not equal in canonical mode. The latter is an
opaque UUID. The former is selected from sorted provider aliases, preferring
`addressingJid` when that value is an alias. Messages continue to expose the
provider Chat ID on which they arrived. No canonical read returns a provider
Chat ID as its canonical entity identity, but the legacy DTO makes accidental
use possible because `chatId` remains required.

Canonical Conversation readiness proves every retained Message association, so
the new message contract can require `conversationId`. The mixed-deployment
legacy `ProjectedMessage` contract cannot make it required and remains
unchanged.

`addressingJid` is stored separately from `conversation_id`. Direct
conversations obtain it from the canonical Contact preferred JID; other types
use their isolated provider Chat ID. It is not used by identity reconciliation,
redirect lookup, totals, or cursor scope.

The existing archive, unarchive, mute, unmute, pin, and unpin services parse the
request `chat` directly as a provider JID and send a WhatsApp app-state mutation.
HistorySync accepts provider `MessageInfo`, including its Chat JID and provider
message ID. These commands do not resolve canonical UUIDs or write authoritative
projection state after the provider acknowledgement. Their routes also retain
upstream TODO markers. They are provider-chat commands, not yet safe canonical
Conversation commands.

Canonical list cursors are version 2 and ordered by
`(lastActivityAt, conversationId)`. Canonical message cursors are version 2,
contain the canonical Conversation UUID, and are ordered by
`(providerTimestamp, messageId)`. Legacy, cross-resource, and
cross-conversation cursors fail with `invalid_cursor`.

Projection read errors are abstraction-neutral (`projection_not_ready`,
`invalid_cursor`, `not_found`, and `internal_error`). Legacy command validation
still contains Chat wording; it remains part of the legacy provider adapter and
is not copied into the Conversation contract.

Repository and GitHub code searches found no owned consumer outside
`omniwa-console` that references these exact canonical fields or read paths.
That is not proof that third-party or deployed clients do not exist. The backend
had no route-labelled request metric or legacy-endpoint usage counter. This
decision adds bounded contract/operation/status-class metrics so retirement can
be based on backend evidence; historical traffic still requires access logs or
an external gateway.

## Alternatives

### A. Keep the backend unchanged and translate names in the frontend

This has the smallest deployment change and preserves every client, cursor,
cache key, and deep link. It leaves the public backend contract misleading,
requires every client to understand provider versus canonical identity, and
keeps code generation centered on Chat. It also makes future clients repeat the
same translation and provides no capability signal for the product contract.

### B. Rename the existing contract in place

This minimizes long-term duplicate API surface, but breaks paths, generated
clients, stored deep links, response schemas, capability checks, and provider
command semantics. Aliasing `chatId` to a UUID would be a semantic break even if
the JSON name stayed unchanged. Rollback would be unsafe after clients stored
new cursors or identities. This option is rejected.

### C. Add a Conversation contract and phase out Chat reads

This adds four routes and two response DTOs while sharing all canonical
application and repository logic. It preserves legacy paths, fields,
capability, cache keys, and cursors. The costs are parallel documentation,
generated-client surface, and the risk of adapter drift. Shared private read
helpers, parity tests, and an OpenAPI regression test control that risk. This
option is selected.

## Decision

Add these canonical reads:

- `GET /conversations`
- `GET /conversations/{conversationRef}`
- `GET /conversations/{conversationRef}/messages`
- `GET /conversations/{conversationRef}/messages/{messageId}`

`conversationRef` accepts the canonical UUID, a current or absorbed provider
Chat ID alias, or a one-hop absorbed Conversation UUID through the existing
instance-scoped resolver. Every response normalizes to the canonical
`conversationId`.

`ProjectedConversation` requires `conversationId`, `type`, and `unreadCount`.
It uses `aliases` rather than `chatAliases`; `addressingJid` remains optional
provider-addressing metadata. Authoritative projected name, activity, unread,
archive, pin, mute, and disappearing-timer fields are carried without transport
recomputation.

`ProjectedConversationMessage` requires `messageId`, `conversationId`,
`direction`, `messageType`, `providerTimestamp`, and `provenance`. It does not
contain `chatId`. Optional `providerChatId` preserves arrival/provenance metadata
without presenting it as the entity identity.

The scoped message-detail route is the canonical replacement for consumers of
the legacy `GET /message/{messageId}` projection DTO. It verifies membership in
the resolved Conversation instead of treating a provider message ID as a
complete resource scope.

The legacy `ProjectedChat`, `ProjectedMessage`, `ChatType`, `chatAliases`,
`chatId`, `/chat/*` routes, and canonical behavior remain compatible. The three
legacy Chat read operations are deprecated in OpenAPI; provider commands remain
available and are not claimed as canonical Conversation commands.

Add `canonical_conversation_identity` as the preferred capability.
`canonical_chat_identity` remains a deprecated compatibility alias. Both use
the identical canonical configuration gate, required resources, durable Contact
and Conversation checkpoints, association validation, and authoritative unread
readiness function. During the compatibility phase, a new binary advertises
both or neither; neither capability is derived from the version string. After
the measured deprecation window, setting `WA_LEGACY_CHAT_READS_ENABLED=false`
retires the three legacy Chat reads and legacy projected-message detail with
machine-readable HTTP 410 responses, and removes the legacy capability alias
while the preferred capability and canonical behavior remain unchanged.
Keeping bounded tombstone routes for the retirement release makes residual
authenticated attempts measurable before physical deletion in a later major
release.

## Command decisions

| Legacy command | Semantics and final target | Decision |
|---|---|---|
| archive / unarchive | Provider Chat app-state mutation; target is provider Chat JID | Keep `/chat/*`; defer Conversation endpoint |
| mute / unmute | Provider Chat app-state mutation; target is provider Chat JID | Keep `/chat/*`; defer Conversation endpoint |
| pin / unpin | Provider Chat app-state mutation; target is provider Chat JID | Keep `/chat/*`; defer Conversation endpoint |
| history-sync | Provider message operation; target is `MessageInfo.Chat` plus provider message ID | Keep provider terminology; defer |
| message operations | Provider message identity plus JID fields, spread under `/message/*` | Outside this additive read contract |

For a future Conversation command, direct Conversations should resolve to
`addressingJid`; groups, newsletters, and broadcasts require type-specific
provider validation. The command must acknowledge the provider and update or
invalidate the projection authoritatively. No command may send a canonical UUID
to whatsmeow.

## Compatibility, rollout, and rollback

Deploy the additive binary with `WA_CANONICAL_CHAT_IDENTITY_ENABLED=false` if a
cohort is not already canonical-ready. Existing instances and clients continue
legacy Chat behavior. After Contact, Chats, Messages, structural backfill, and
unread readiness are authoritative, enabling the existing flag advertises both
canonical capabilities and serves the new endpoints. Clients switch only when
`canonical_conversation_identity` is present and should keep their legacy path
fallback during mixed-version rollout.

Monitor `omniwa_conversation_api_requests_total` and
`omniwa_conversation_api_request_duration_seconds` for bounded
new-versus-legacy read traffic, plus projection health, reconciliation failures,
API error rate, and latency. These metrics never label instance, Conversation,
Chat, message, or provider identities. Do not disable legacy reads until their
successful authenticated traffic has remained at zero for the agreed
deprecation window and every supported instance advertises the preferred
capability.

The first rollback sets `WA_LEGACY_CHAT_READS_ENABLED=true` and restarts, which
restores the four legacy projection reads and capability alias without changing
canonical data. Disabling `WA_CANONICAL_CHAT_IDENTITY_ENABLED` remains the deeper rollback
after clients have returned to legacy reads. There is no database migration,
backfill, data deletion, or cursor rewrite to reverse.

## Consequences

- Product language and generated clients receive a canonical Conversation
  contract without weakening provider provenance.
- Canonical totals, unread, alias aggregation, message deduplication, redirects,
  instance isolation, and cursor scope remain backend-owned.
- Two public read surfaces coexist temporarily, but shared query helpers and
  parity tests prevent independent business logic.
- Command migration remains deliberately deferred because the existing provider
  operations are not authoritative. Legacy usage telemetry and a reversible
  read-removal control are included in the migration contract.
