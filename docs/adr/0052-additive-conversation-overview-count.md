# ADR 0052: Additive Conversation overview count

## Status

Accepted

## Context

ADR 0007 introduced the persisted `GET /server/overview` response before
Conversation became the public product domain. Its `projections.chats` field now
names the storage projection rather than the canonical entity exposed by the
public API. The Console labels that value as Conversations, but generated clients
still expose the legacy field name.

The repository and database correctly retain Chat terminology for the provider
projection and historical table. Renaming those internals or replacing the public
field in place would create unnecessary migration risk and break existing overview
consumers.

## Decision

Add `projections.conversations` to the overview response. Both
`projections.conversations` and the deprecated compatibility alias
`projections.chats` are populated from the same persisted canonical Conversation
count in the service layer.

The handler does not issue an additional query or recompute the value. The
repository field and database implementation remain named `Chats` because they
describe the historical provider projection and changing them provides no public
contract benefit.

No capability is required: the two fields are response-shape aliases, not
different behaviors. Consumers should prefer `conversations` when present and may
fall back to `chats` during mixed-version rollout.

Existing bounded Conversation API telemetry remains authoritative for contract
usage and latency. It uses only allowlisted contract, operation, and status-class
labels and does not include instance, Conversation, provider, token, or JID data.
The current binary does not emit legacy Chat contract series.

## Alternatives

### Keep only `chats`

Rejected because it perpetuates product terminology that no longer matches the
canonical public entity and forces every new client to add a semantic adapter.

### Rename `chats` to `conversations` in place

Rejected because it is a breaking public API change and provides no safe
mixed-version rollout for generated clients.

### Rename persistence and projection-state resources

Rejected for this change. Values such as the `projected_chats` table and `chats`
projection-health resource are operational and persistence identifiers. They need
a separate impact analysis before any migration.

## Consequences

- New clients can use canonical Conversation terminology.
- Existing clients continue to receive the same `chats` value.
- The response temporarily carries two equal counters.
- The OpenAPI schema explicitly describes `chats` as deprecated compatibility.
- There is no database migration, backfill, capability change, or extra query.

## Rollout and rollback

Deploy the additive response and verify representative admin- and instance-scoped
overview calls return equal `conversations` and `chats` values. Frontends then
prefer `conversations` while retaining a `chats` fallback across the mixed-version
window.

Rollback by redeploying the previous immutable image. Older clients remain safe
because `chats` is unchanged. Clients must not require `conversations` until the
backend rollout is complete. Removing `chats` requires a later ADR, usage evidence,
and explicit breaking-contract approval.
