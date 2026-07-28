# ADR 0030: Group management contract foundation

## Status

Accepted as an additive, disabled-by-default foundation. Public Group API
cutover, permission decisions, command execution, audit reads, and shared-media
photo behavior remain separate rollout stages.

The normalized directory/detail, tri-state permission reads, bounded member
directory, journaled commands, and public audit reads are now implemented
behind the same disabled-by-default gate. Shared-media photos are implemented
behind an additional disabled-by-default gate. The authoritative instance-wide
directory summary is part of the normalized read surface.

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

The command stage adds a forward migration 32 because create and join commands
do not have a trustworthy Group JID before the provider responds. Only
`created` and `joined` commands may be persisted with a null Group JID. A
terminal confirmed outcome sets the resolved Group JID; earlier audit events
remain append-only and unresolved. Other command types continue to require a
canonical Group JID at insertion.

Every mutation uses strict, bounded JSON and command-time projection
revalidation. Existing-member participant operations resolve an opaque member
ID inside the instance-and-group scope; add accepts only canonical WhatsApp
user JIDs. The journal is written before provider admission, an instance-wide
outbound limiter is acquired before execution, and the command transitions to
`executing` immediately before the single provider call. Provider rate limits
are returned as HTTP 429 with `Retry-After`. Other uncertain provider outcomes
are stored as `unknown`, returned as such, and are never retried.

Create and participant mutations return bounded per-participant outcomes. Join
returns `joined`, `already_member`, `approval_required`, `rejected`, or
`unknown`; because the current provider library does not expose a reliable
approval-request result, membership is claimed only after a successful
post-command group-info confirmation. Public audit reads expose terminal
command type, status, bounded summary, actor type, and timestamp. They never
expose invite links, participant aliases, raw provider payloads, credentials,
or idempotency keys.

Invite-link permission and cache availability are separate facts. The
normalized `GroupDetail.inviteLink.available` field reports only whether a
non-empty link is currently cached in the instance-scoped Groups projection;
it does not claim that the provider has no link. `actions.readInviteLink` and
`actions.resetInviteLink` remain advisory permission decisions derived from the
same normalized Group record. A projection-backed read preserves its existing
string success payload, revalidates `readInviteLink` before returning the
cached secret, and adds projection metadata. A missing Group row is
`group_not_found`; an existing Group with no cached link is
`group_invite_link_not_found` with bounded availability and projection details.
This additive error split was selected instead of changing successful `data`
from a string to an object, which would break existing generated clients.

A confirmed invite-link reset writes the returned link through to the Group
projection before the command is recorded as `completed`. If the provider may
have reset the link but write-through cannot be confirmed, the command is
recorded as `unknown`, returned for operator review, and is never retried
automatically. Replaying the same `Idempotency-Key` returns the stored unknown
outcome without a second provider call.

The photo stage adds migration 33 and the independent
`WA_GROUP_PHOTO_ASSETS_ENABLED` gate. `POST /group/photo` accepts only an
instance-owned ready device-upload `mediaAssetId` when the gate is enabled.
The server rereads the private canonical JPEG/PNG, verifies byte and pixel
bounds, center-crops it, and emits a bounded 640-by-640 JPEG for one provider
call. A time-bounded `group_photo_pending` reference fences cleanup between
validation and provider admission. Confirmed success writes the provider
picture ID plus opaque asset ID to the Group projection, replaces the previous
durable `group_photo` owner reference, and removes the pending reservation.
Unknown outcomes are never retried; their reservation expires after 24 hours.
Neither image bytes nor storage metadata enter the journal or audit summary.

`GET /group/summary` performs one instance-scoped aggregate over the same
`projected_groups` rows exposed by the normalized directory. It never derives
global metrics from a cursor page and never calls WhatsApp. `total` counts all
directory records, including unavailable and dissolved records retained by the
projection. `active` and `suspended` include only rows with an explicit matching
live state. Community, subgroup, and admins-only-send counts include only rows
whose corresponding projected fact is known; missing facts do not become false.
`updatedAt` is the authoritative Group reconciliation timestamp, not a request
time. The existing `(instance_id, group_id)` primary key scopes the aggregate,
so no new migration or index is required. `group_summary` is advertised only
when the normalized Group gate is enabled and schema version 4 is serving.

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
- Shared-media Group photos reuse the private asset lifecycle without exposing
  storage metadata. A provider success followed by a local projection/reference
  failure is reported as `unknown` and requires operator review; it is never
  retried automatically.
- The summary query is intentionally on demand. It is not added to directory
  rows, so directory pagination remains free of aggregate N+1 work.

## Rollout and rollback

Apply migrations 30, 31, 32, and 33 with
`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED=false`. Verify
participant public IDs are populated and unique, journal constraints are
present, member-directory indexes are valid, and existing Group projection
reads and mutations remain unchanged. Because index creation runs in the
versioned migration transaction, first canary the deploy against production-like
participant volume and observe migration lock duration before broader rollout.
Enable normalized reads, members, and commands/audit in that order. Keep
`WA_GROUP_PHOTO_ASSETS_ENABLED=false` until the shared private bucket, device
upload, normalized Group management commands, and Console contract are
verified. Enable it after the Group management gate and require the separately
advertised `group_photo_assets` capability before Console sends an asset ID.
Require `group_summary` before showing global Group metrics. If the capability
is absent or the projection is not serving, omit those metrics rather than
calculating them from the current page.

Disable the photo gate first to restore the legacy photo handler, then disable
the management gate if needed. Leave additive columns, references, and tables
in place; repair schema defects with a forward migration. Keep the shared
bucket reachable while durable photo references exist, and do not drop journal
or audit rows during application rollback.
