# ADR 0037: Canonical conversation serving and authoritative unread

- Status: Superseded in part by ADR 0049
- Date: 2026-07-29

## Context

The migration 37 shadow model can associate PN and LID Chat rows with one
canonical Contact, but public reads remain provider-Chat scoped. Its unread
aggregate is deliberately non-authoritative: summing alias snapshots can count
one message twice, taking the maximum can discard distinct unread messages, and
choosing a display-name or preferred alias is not an identity proof.

Frontend grouping cannot correctly own message aggregation, receipts,
pagination, redirects, totals, or concurrent PN/LID reconciliation. The
backend already owns instance-scoped Contact identity and the tenant-wide
provider message key.

## Decision

Migration 38 adds nullable message-level `is_unread`, Chat snapshot identity,
and Chat unread-readiness state. A complete history snapshot classifies the
newest N retained incoming messages only when at least N are available. Live
incoming and outgoing events write true and false respectively. Incoming
read-self receipts and successful local mark-read commands transition messages
to read idempotently. Canonical unread is counted from distinct retained
provider message IDs. Missing evidence and unread retention deletion invalidate
readiness rather than guessing.

Serve canonical reads only when the same instance advertises
`canonical_chat_identity`. Add `conversationId`, `chatAliases`, and
`addressingJid` to `ProjectedChat`, and `conversationId` to projected messages.
List, detail, and message-history reads resolve canonical IDs, provider aliases,
and one-hop absorbed redirects entirely in PostgreSQL. Canonical cursors use a
new version and bind message pagination to the canonical conversation.

Keep legacy reads and cursors unchanged while the capability is absent. Keep
groups, newsletters, broadcasts, and unknown chats isolated. Never merge by
name, phone text, timestamp, or content.

## Alternatives

- Frontend grouping by Contact ID was rejected because it cannot provide
  authoritative totals, unread, message deduplication, receipts, redirects, or
  pagination.
- Alias unread `sum`, `max`, or primary-alias selection was rejected because
  none proves which provider messages overlap.
- Rewriting historical `chatId` values was rejected because it would break
  existing commands, paths, and stored cursors. An additive `conversationId`
  keeps the rollout reversible.

## Consequences

- Instances can remain in legacy mode independently until Contact, Chat,
  Message, structural backfill, and unread evidence are all ready.
- A complete RECENT or FULL history sync is required to raise Messages to
  schema version 3 after rollout.
- Retained message identity, rather than text or timestamp, defines dedupe.
- Local mark-read acknowledges WhatsApp first. If projection write-through then
  fails, the request is not retried and Messages is marked stale so the
  capability fails closed until reconciliation.
- An unread message expiring from retention removes canonical readiness; a
  later authoritative history snapshot may restore it.

## Rollout

1. Deploy the additive migration 38 binary with
   `WA_CANONICAL_CHAT_IDENTITY_ENABLED=false`.
2. Verify migrations 34 through 38 and canonical Contact reconciliation per
   instance.
3. Enable canonical Chat identity with bounded backfill settings.
4. Complete a valid RECENT or FULL HistorySync and verify Messages schema 3 is
   ready, unread validation has no incomplete conversations, and structural
   backfill is complete.
5. Confirm `canonical_chat_identity` through the instance-targeted capability
   endpoint before updating any client behavior.

## Rollback

Disable `WA_CANONICAL_CHAT_IDENTITY_ENABLED` and restart. This immediately
restores legacy read and cursor behavior and removes the conditional
capability. Migration 38 columns and message-level state remain additive for a
later retry. Do not roll back to a binary that reports Messages schema 2 as
current while clients still rely on the canonical capability; drain or disable
the capability first. No automatic data deletion is part of rollback.
