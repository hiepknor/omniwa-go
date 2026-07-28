# ADR 0030: Group management contract foundation

## Status

Accepted as an additive, disabled-by-default foundation. Public Group API
cutover, permission decisions, command execution, audit reads, and shared-media
photo behavior remain separate rollout stages.

The normalized directory/detail, tri-state permission reads, and bounded member
directory are now implemented behind the same disabled-by-default gate.
Commands, audit reads, and shared-media photos remain later stages.

## Context

The current Group HTTP surface returns whatsmeow provider structures, expands
participants in directory reads, and exposes mutation success without durable
per-target outcomes. Console must instead consume stable public DTOs and
backend-owned permission decisions without querying WhatsApp from read paths.

Changing the existing routes in place is a breaking public-contract decision.
Group mutations also cross a PostgreSQL and WhatsApp boundary that cannot be
made atomic. Retrying after an admitted provider call could duplicate an
irreversible operation, while writing audit only after the call could lose the
operation on process failure.

## Decision

Use the existing Group route paths and gate the normalized contract with
`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED`, which defaults to false. Deploy the
backend with the gate disabled, deploy a compatible Console, and enable the
contract only after the client is ready. Capability advertisement is added by
later stages only when each complete surface is serving; the foundation alone
advertises nothing.

Projection-backed permission decisions are backend-owned advisory results with
three states: `allowed`, `denied`, and `unknown`. They never call WhatsApp.
Mutation handlers fail closed when projection or actor identity is insufficient
and still submit the provider command at most once after command-time
revalidation.

Projected participants receive an instance-and-group-scoped UUID `public_id`.
Console uses this opaque identifier for existing-member operations, while add
operations accept canonical WhatsApp user addresses. Provider aliases remain
internal.

Persist every future management mutation in `group_management_commands` before
provider admission. Store only a SHA-256 digest of an optional idempotency key,
a public-safe request fingerprint, bounded public-safe outcome data, and
secret-free execution lifecycle timestamps. Append `group_management_audit_events` in
the same database transaction as each journal transition. A bounded recovery
pass changes abandoned `executing` commands to `unknown` and never retries
them.

The journal lifecycle is:

```text
requested -> executing -> completed | partially_completed | failed | unknown
requested -------------> failed | unknown
```

Audit summaries are supplied separately from command outcomes. This prevents a
future one-time command result, such as a reset invite link, from entering the
audit trail. Raw provider payloads, participant aliases, media bytes, object
keys, invite links, credentials, and idempotency keys are forbidden from both
tables.

Migration 30 is expand-only. It adds participant public IDs, nullable actor
membership and group picture projection fields, the command journal, and the
append-only audit table. It deliberately does not advance a serving projection
schema version or publish a capability.

The read stage advances the Groups projection schema to version 4 after a full
authoritative reconciliation. It adds no migration: version 4 consumes the
nullable projection fields and participant identity indexes introduced by
earlier migrations. Directory queries select Group rows plus only the bounded
current-actor and owner references needed for decisions; they do not hydrate
participant collections. Alias resolution reads the persisted contact identity
graph and never falls back to WhatsApp.

The member-directory stage adds migration 31 with partial B-tree indexes for
instance-and-group-scoped display-name ordering and prefix search. Its keyset
cursor orders by normalized display name and opaque participant public ID, so
members without display names remain pageable without exposing an alias. The
read returns only active projected participant rows and performs no live
provider fallback. `group_members_projection` is advertised only when the
feature gate is enabled and Groups schema version 4 is ready.

## Alternatives considered

A `/v2` route tree was rejected because the product requires one Group route
surface. An immediate in-place response replacement was rejected because it
would break deployed clients without a rollback gate. Best-effort post-command
audit was rejected because a process failure can lose an already-admitted
mutation. Automatic recovery retry was rejected because provider admission is
not safely idempotent.

## Consequences

- The feature gate is a behavior rollback boundary, not proof that a capability
  is ready.
- Command persistence can prove what was requested and what outcome is known,
  but it cannot prove that an unknown provider operation did or did not apply.
- Public participant IDs are stable for the lifetime of a projected participant
  row and do not expose Phone, LID, or provider JID aliases.
- Later stages must use bounded strict-JSON handlers, normalized errors with
  request IDs, scoped cursors, and command-time permission revalidation.
- Shared-media Group photos require a later media-reference owner and storage
  integration; this foundation stores only nullable provider picture metadata.

## Rollout and rollback

Apply migrations 30 and 31 with
`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED=false`. Verify
participant public IDs are populated and unique, journal constraints are
present, member-directory indexes are valid, and existing Group projection
reads and mutations remain unchanged. Because index creation runs in the
versioned migration transaction, first canary the deploy against production-like
participant volume and observe migration lock duration before broader rollout.
Later stages enable normalized reads, members, commands/audit, and photos in
that order.

Disable the feature gate to roll behavior back. Leave additive columns and
tables in place; repair schema defects with a forward migration. Do not drop
journal or audit rows during application rollback.
