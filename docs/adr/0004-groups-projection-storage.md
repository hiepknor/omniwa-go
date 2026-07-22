# ADR 0004: Groups projection storage and ordering

- Status: Accepted
- Date: 2026-07-22

## Context

Group reads must move away from live WhatsApp queries without returning a
false empty result before synchronization. Group events can be duplicated,
delayed, or delivered out of order, and full snapshots can race with newer
participant changes.

## Decision

Store normalized group metadata in `projected_groups` and participant identity
and roles in `projected_group_participants`. Both tables are instance-scoped,
retain source occurrence and local synchronization timestamps, and represent
deletion with tombstones. Cached invite links carry their own update timestamp.

Snapshot and delta application is transactional. Migration 4 adds an internal
JSONB version map. The `_snapshot` version is the fallback for all metadata,
while each changed field receives its own version. A late snapshot can fill
fields that have no newer value without overwriting a later delta in another
field. Participant rows retain independent versions, so snapshot replacement
also preserves later joins and role changes.

A stable source event key breaks equal-timestamp ties, so ordering compares
`(source_occurred_at, source_event_key)` rather than arrival order. A stale
snapshot, delta, or delete therefore cannot roll back newer state. Duplicate
events are safe to replay.

Rows distinguish unknown values with nullable columns. API handlers must not
interpret a partial row as a completed initial sync; projection state remains
the authority for `not_started`, `syncing`, `ready`, `stale`, and `failed`.

## Rollout and rollback

Migrations 3 and 4 are additive. A lease-based background worker applies
`JoinedGroup` snapshots and `GroupInfo` metadata, settings, participant,
invite-link, and delete deltas, and shuts down through the application context.
It claims only event types with a registered projector.

Initial reconciliation starts asynchronously after a client connects and uses
the shared information-query guard. It marks the instance `syncing`, captures a
version fence before the provider query, applies the authoritative list, and
tombstones missing groups. Events after the fence win the merge even if the
query completes later. Only a complete successful run marks the projection
`ready`; failure becomes `failed` for initial sync or `stale` when prior
reconciled data exists.

Each connected client owns one cancellable periodic reconciliation loop. Runs
are non-overlapping, use a stable per-instance jitter around
`WA_GROUP_RECONCILE_INTERVAL` (default `6h`), and continue to use the shared
information-query guard. Disconnect, logout, stream replacement, client
restart, and application shutdown cancel the loop and any in-flight query.
Setting the interval to `0` disables periodic runs without disabling the
initial reconciliation.

Migration 5 completes the compatibility read model before the API cutover. It
adds the group name/topic setter metadata, topic and announce version IDs,
incognito state, provider participant count, creator country code, and default
membership approval mode that are present in the existing `GroupInfo`
response. Snapshot and delta field versions cover the related metadata as one
atomic logical field, preventing a late event from mixing values from
different versions.

`GET /group/list` and `POST /group/info` use a projection reader once an
instance has completed reconciliation with schema version 3 or newer. Before
that point they return `projection_not_ready` and never issue a request-path
provider query, so deployment and reconnection cannot produce a false empty
result or amplify upstream load. A ready, stale, or currently
resyncing projection with prior reconciled data is served without probing
WhatsApp and includes additive `source`, `syncStatus`, and `lastSyncedAt`
metadata. The `groups_projection` capability is published only for a ready
schema-3 projection. List reads load groups and participants in two bounded
database queries rather than one query per group.

The additive `GET /group/search` endpoint provides case-insensitive prefix
search over normalized group JIDs and names. It uses bounded keyset pagination
ordered by group JID. Opaque cursors are versioned and bound to both the
instance and normalized query, so they cannot be reused across tenants or
filters. Migration 14 adds partial prefix and page indexes; the original list
and info paths and their response shapes remain unchanged.

Confirmed create, join, leave, name, topic, settings, participant, membership
request, and invite-link mutations write through to the projection before the
success response completes. Create and join enrichment queries use the shared
query guard; a failed enrichment cannot turn a completed mutation into an API
failure. Projection-write failures likewise do not invite an unsafe mutation
retry: they are logged with stable error codes, mark the resource stale, and
are repaired by reconciliation. Invite-link reads use the cached projection
when present; cache misses return `not_found` without a provider query, and
reset remains a live mutation whose returned link replaces the cache.

Application rollback leaves the new tables unused. Instance deletion cascades
to groups, and group deletion cascades to participants. Physical cleanup is not
part of the request path.

## Consequences

- Group and participant state survives restart and can be queried without the
  provider once synchronization is ready.
- Snapshot replacement, late delivery, duplicates, and deletion have explicit
  database ordering semantics.
- Timestamp ties converge deterministically by stable event key.
- Provider timestamps remain a trust boundary; reconciliation supplies a newer
  authoritative snapshot when events are missing.
- Multiple replicas can process supported snapshots concurrently; inbox leases,
  fencing tokens, deterministic storage versions, and atomic monotonic
  projection-state updates prevent cross-replica rollback.
