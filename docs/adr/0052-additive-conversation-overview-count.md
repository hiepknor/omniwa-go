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

Add `projections.conversations` to the overview response. It counts active rows
in the canonical `projected_conversations` projection. The deprecated
`projections.chats` compatibility field continues to count active provider Chat
projection rows exactly as it did before this decision.

The repository obtains both counts inside the existing repeatable-read, read-only
snapshot transaction. The handler does not query or recompute either value.
Provider aliases can make `chats` larger than `conversations`; absorbed or
not-yet-canonical provider rows must not become public entity counts.

No capability is required: both fields are persisted overview counters, while
their names and documented semantics identify the canonical entity count versus
the retained provider-row count. Consumers should prefer `conversations` when
present and may fall back to `chats` during mixed-version rollout.

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
- Existing clients continue to receive the same provider-row `chats` value.
- The counters can differ when several provider Chat rows resolve to one
  canonical Conversation or a provider row has not been canonicalized.
- The OpenAPI schema explicitly describes `chats` as a deprecated provider-row
  compatibility count that can exceed `conversations`.
- There is no database migration, backfill, or capability change. The existing
  repeatable-read snapshot adds one bounded count over the indexed canonical
  projection table.

## Rollout and rollback

Deploy the additive response and verify representative admin- and instance-scoped
overview calls return a `conversations` value equal to canonical list
`meta.total` for the same scope. Frontends then prefer `conversations` while
retaining a `chats` fallback only for older backend revisions during the
mixed-version window.

Rollback by redeploying the previous immutable image. Older clients remain safe
because `chats` is unchanged. Clients must not require `conversations` until the
backend rollout is complete. Removing `chats` requires a later ADR, usage evidence,
and explicit breaking-contract approval.
