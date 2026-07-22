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

Snapshot application is transactional. A snapshot may replace metadata and the
participant set only when its source version is at least as new as the stored
group version. A stable source event key breaks equal-timestamp ties, so
ordering compares `(source_occurred_at, source_event_key)` rather than arrival
order. Participant upserts and missing-participant tombstones have the same
guard. A stale snapshot or delete therefore cannot roll back newer state.
Duplicate snapshots are safe to replay.

Rows distinguish unknown values with nullable columns. API handlers must not
interpret a partial row as a completed initial sync; projection state remains
the authority for `not_started`, `syncing`, `ready`, `stale`, and `failed`.

## Rollout and rollback

Migration 3 is additive. This increment creates storage only and does not
change existing group API reads or enable `groups_projection`. The worker,
initial reconciliation, write-through mutations, and compatible read cutover
must be delivered and verified before the capability becomes ready.

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
