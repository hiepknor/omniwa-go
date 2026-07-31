# ADR 0049: Separate Conversation identity and unread readiness

## Status

Accepted

## Context

Canonical Conversation serving originally used one readiness predicate for two
different claims: the Chat/Message alias graph was structurally canonical, and
every active Conversation had an authoritative provider unread snapshot. A
message-derived direct chat can be valid even when the provider Contacts store
has no row for it. Local whatsmeow may still contain an authoritative PN/LID
mapping, but the Contact backfill scanned only projected Contacts and therefore
could not materialize or associate that mapping.

The unread requirement created a separate liveness problem. Outgoing-only or
metadata-light conversations can lack a complete provider unread snapshot
indefinitely. Blocking canonical identity reads on that evidence made a valid
identity graph unavailable and did not improve unread correctness.

## Decision

On each successful connection, reopen the completed bounded Conversation pass.
Before associating each direct PN or LID chat, resolve only the local whatsmeow
mapping store. When a complete authoritative pair exists, apply an idempotent
identity-only Contact patch. The existing Contact and Conversation repository
transactions then link aliases, absorb duplicates, and re-associate messages.
Group, newsletter, broadcast, and unknown identifiers are not contact-enriched.

`canonical_conversation_identity` now proves projection resource readiness,
completed Contact and Conversation checkpoints, and structural association
validation. It gates canonical reads and Conversation commands.

Add `authoritative_conversation_unread`. It is advertised only when canonical
identity is ready and every active Conversation has authoritative unread
evidence. Add required `unreadAuthoritative` to `ProjectedConversation`.
`unreadCount` remains the best-known projection value when the flag is false;
consumers must not represent it as provider-authoritative.

The pass remains bounded, leased, restartable, instance-scoped, and free of
provider network reads. No historical migration is edited and no new database
migration is required.

## Alternatives considered

Keep the combined readiness predicate. This preserves the smallest contract but
can permanently suppress canonical reads for an unrelated missing unread
snapshot.

Mark missing snapshots as authoritative zero. This improves availability by
inventing certainty and was rejected because it can silently under-report
unread state.

Remove unread from readiness without exposing quality. This makes identity live
but leaves consumers unable to distinguish authoritative from best-known
counts. The explicit field and capability avoid that ambiguity.

Create provider Contacts from names, phone text, or message heuristics. This was
rejected. Only an authoritative local PN/LID mapping may merge direct identities.

## Consequences

Canonical reads can serve structurally valid conversations while unread
authority is partial. Strict JSON consumers must tolerate the additive response
field and capability. Operators can diagnose the two readiness dimensions
independently. Reopening a completed pass adds bounded connection-time database
work but uses the existing batch limits and cannot steal an active lease.

## Rollout and rollback

Deploy first with `WA_CANONICAL_CHAT_IDENTITY_ENABLED=true` on one instance and
verify canonical totals, alias absorption, message deduplication, both
capabilities, and cross-instance isolation. Rollback disables the flag or
redeploys the previous image. The persisted graph remains compatible and no
down migration is needed.
