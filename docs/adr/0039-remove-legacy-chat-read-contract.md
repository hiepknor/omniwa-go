# ADR 0039: Remove the legacy Chat read contract

> The combined identity/unread readiness predicate in this ADR is superseded
> by ADR 0049. The legacy Chat read removal remains active.

- Status: Accepted
- Date: 2026-07-29

## Context

ADR 0038 introduced the canonical Conversation public API beside the older Chat
read API. The compatibility surface consisted of three `/chat/*` reads, the
unscoped projected-message detail read, two legacy DTOs, the
`canonical_chat_identity` capability alias, and a rollout flag that could turn
those reads into HTTP 410 tombstones.

The canonical API has a backend-owned Conversation identity, resolves absorbed
PN/LID and provider Chat aliases, aggregates and deduplicates messages, scopes
cursors to the canonical Conversation, and exposes `addressingJid` only as
provider-addressing metadata. A development-stack retirement rehearsal verified
the canonical list, detail, message list, and scoped message detail reads while
the four compatibility reads returned their intended tombstones. Provider Chat
commands remained available.

Physical removal is an L3 breaking public-contract change. The previously
planned usage-observation window cannot prove safety for clients outside owned
telemetry, and removing compatibility now can break an unobserved external
consumer. The project owner explicitly approved accepting that risk and
removing the surface after the rehearsal rather than retaining another release
of tombstones.

## Decision

Remove these public operations:

- `GET /chat/list`
- `GET /chat/info/{chatId}`
- `GET /chat/{chatId}/messages`
- `GET /message/{messageId}`

Remove their handler methods, public `ProjectedChat` and `ProjectedMessage`
adapter DTOs, version-1 adapter cursors, `canonical_chat_identity` capability
alias, `WA_LEGACY_CHAT_READS_ENABLED` rollout setting, HTTP 410 tombstone, and
the `legacy_chat` API metric label. Remove the operations and schemas from the
generated OpenAPI contract instead of documenting dead routes.

Keep the canonical Conversation reads and the single
`canonical_conversation_identity` capability. Its authoritative readiness
continues to depend on the same projection, reconciliation, association, and
unread checks; capability support is never inferred from a version.

Keep archive, unarchive, mute, unmute, pin, unpin, and history-sync under
`/chat/*`. They are provider adapter commands whose final target is a provider
JID or provider message identity, not a canonical Conversation UUID. Also keep
message delivery receipts, internal provider and persistence Chat terminology,
`chat_id` provenance, PN/LID aliases, repositories used by projection and
backfill, and all published migrations.

This is an API/application-layer contraction only. It performs no schema or
data migration and does not delete canonical or provider data.

## Alternatives

### Keep the compatibility surface longer

This is operationally safest and would provide a larger observation window,
but it preserves misleading generated clients and two names for one product
domain. It cannot identify all third-party clients without complete external
traffic telemetry. Rejected by explicit owner decision after the development
rehearsal.

### Rename Chat in place

Changing the old paths and fields in place would still break generated clients,
cursors, deep links, and stored provider identifiers while making provider
command semantics ambiguous. Rejected.

### Remove only the routes and retain aliases indefinitely

Keeping unused DTOs, capability aliases, flags, tombstones, and labels would
leave dead contract states and invite accidental reintroduction. Rejected in
favor of deleting the complete compatibility boundary while retaining genuine
provider and persistence concepts.

## Consequences

- Canonical clients have one public product model and one capability signal.
- Requests to removed routes receive the router's normal not-found response;
  `legacy_contract_removed` is no longer a supported error contract.
- Existing consumers of the removed operations or schemas must migrate before
  deploying this release. Old Chat cursors cannot be used with Conversation
  endpoints and correctly fail as `invalid_cursor`.
- Provider commands and stored provider identifiers are unaffected.
- Maintenance and generated-client surface shrink, and adapter drift becomes
  impossible.
- Historical `legacy_chat` metric series may remain in Prometheus, but the new
  binary no longer emits them.

## Rollout and rollback

Regenerate and review OpenAPI, run contract and canonical identity tests, then
deploy the immutable image to development before promoting it. Verify canonical
list, detail, history, scoped message detail, projection-not-ready behavior,
cursor scope isolation, capability readiness, and provider command route
presence. Confirm the removed operations are absent from both routing and
OpenAPI.

Rollback is application-only: redeploy the previous immutable image digest
`ghcr.io/hiepknor/omniwa-go@sha256:b59e6273da8bab3c588b584be00d9802c3df027ff3daa5261ce4b79f2ca26100`
with `WA_LEGACY_CHAT_READS_ENABLED=true`. Reverting this change restores the
compatibility routes and alias without a data rollback. Replacement conditions
are a newly discovered supported consumer or a canonical regression that cannot
be forward-fixed safely during rollout.
