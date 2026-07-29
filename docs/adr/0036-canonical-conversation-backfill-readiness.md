# ADR 0036: Fail-closed canonical conversation backfill readiness

- Status: Superseded by ADR 0037
- Date: 2026-07-29

## Context

Migration 37 introduced additive canonical conversation, alias, redirect, and
checkpoint storage. New projection writes maintain shadow associations, but
historical Chats and Messages need a bounded, resumable backfill. A public
capability must not be emitted merely because the schema exists or the process
started.

PN and LID Chat snapshots also contain per-provider-chat unread values. Adding
those values may double-count the same unread messages. Taking their maximum
may discard unread messages that exist only under another alias. Neither is an
authoritative canonical-conversation algorithm.

## Decision

Use the migration 37 per-instance checkpoint with a two-minute lease. Scan
active Chats by provider Chat ID, associate the complete canonical Contact
scope transactionally, and re-parent all retained Messages for each affected
alias. Projection writes and backfill take the same Chat and logical
conversation advisory locks. The operation is idempotent; expired leases can be
reclaimed and committed only by their current owner.

Structural validation rejects missing or mismatched Chat aliases, missing or
mismatched Message associations, redirect chains, orphan aliases, active
conversations without aliases, and direct conversation/Contact disagreement.
Structural completion is durable even when unread remains non-authoritative.

Add the independently configured `canonical_chat_identity` capability, but
advertise it for an instance only when all of the following are true:

1. Contacts, Chats, and Messages projections are serving-ready.
2. Canonical Contact reconciliation is complete at the current version.
3. Canonical conversation structural backfill is complete at the current
   version.
4. Structural validation succeeds at capability evaluation time.
5. Every active canonical conversation has authoritative unread state.

The initial implementation intentionally leaves condition 5 false. It does not
publish the new public Chat behavior before a later unread migration can prove
message-level correctness.

## Consequences

- Backfill can run safely during live ingestion and resume after restart.
- One processed PN alias may associate multiple aliases and Messages; metrics
  count rows actually changed rather than scan items.
- Instances in the same deployment can complete at different times.
- Enabling the environment flag starts structural work but does not guarantee
  capability advertisement.
- Frontends continue legacy Chat behavior while the capability is absent.

## Rollout

1. Deploy migration 37 and a dual-write binary.
2. Enable and complete canonical Contact reconciliation.
3. Set `WA_CANONICAL_CHAT_IDENTITY_ENABLED=true` with conservative batch
   bounds.
4. Observe checkpoint counters and validation failures per instance.
5. Deploy the message-level unread contract in a later change.
6. Verify readiness through instance-targeted `GET /server/capabilities`.

## Rollback

Set `WA_CANONICAL_CHAT_IDENTITY_ENABLED=false` and restart. This stops new
backfill claims and removes the conditional capability evaluator. Existing
shadow associations and checkpoints remain additive and can be resumed later.
Do not delete migration 37 tables or columns while any dual-write binary is
running.
