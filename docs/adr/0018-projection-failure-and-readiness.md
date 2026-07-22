# ADR 0018: Projection failure, dead-letter, and readiness semantics

## Status

Accepted

## Context

Projection events currently return to a retryable failed state after every
processing error. There is no retry ceiling or dead-letter state. A permanent
event can therefore consume worker capacity indefinitely. Projection health is
stored separately and can continue to report `ready` while a relevant event is
failing repeatedly.

Marking an entire projection failed after one transient error would be equally
misleading when a usable snapshot remains available.

## Decision

Extend the durable inbox additively with failure classification, last-attempt
time, retry policy metadata, and a terminal dead-letter state. Retryable errors
use exponential backoff with jitter. Permanent validation or schema errors are
dead-lettered immediately or after a small bounded threshold. No event retries
forever.

Projection serving health is derived from both projection state and durable
work:

- `ready`: the initial barrier completed, schema is current, and lag/dead-letter
  thresholds are clear.
- `syncing`: the initial barrier has not completed.
- `stale`: a usable snapshot exists but lag, reconciliation age, or unresolved
  dead letters exceed policy.
- `failed`: no usable snapshot exists or the projector cannot make progress.

Capabilities remain additive. A stale projection may remain readable only when
the response metadata reports staleness. A projection is never advertised as
ready solely because a stored state row says so.

Admin operations may inspect a safe failure summary, replay an event after a
fix, or discard it with an explicit audited reason. They never edit migration
checksums automatically or expose raw sensitive payloads.

## Consequences

- Poison events stop consuming unbounded capacity.
- Operators gain a repair path and actionable lag/dead-letter metrics.
- Readiness queries become more expensive and require suitable indexes.
- Retry classification must use typed categories rather than raw error text.

## Rollout and rollback

Land additive schema first, then dual-compatible repository code, then worker
behavior, then readiness calculation. Rollback disables new behavior while
leaving additive columns and states intact; forward fixes replay dead letters.

Migration 15 is the schema slice: it adds typed failure metadata, a retry-policy
snapshot, terminal dead-letter timestamps, and work/health indexes. Event
ingestion initializes policy defaults. The worker slice classifies malformed or
unsupported normalized events as permanent, treats unclassified storage and
provider errors as retryable, and uses deterministic jittered exponential
backoff capped at five minutes. Permanent failures dead-letter immediately;
retryable failures dead-letter when the event's persisted attempt ceiling is
reached. Claim-token fencing makes retry and dead-letter transitions atomic.

Older binaries ignore the new nullable/defaulted columns, so image rollback does
not require schema rollback. Rolling back worker behavior leaves terminal rows
intact for later inspection or replay.

The readiness slice aggregates unprocessed inbox work by instance and resource.
It uses `ingested_at`, not the provider's `occurred_at`, so imported history does
not look like worker lag. A ready snapshot becomes effectively `stale` when its
oldest unprocessed item is older than two minutes or any unresolved dead letter
exists. A `not_started` or `syncing` resource with no completed snapshot becomes
effectively `failed`; a syncing resource with a previously completed snapshot
becomes `stale` and remains readable. These derived states are returned without
rewriting the stored synchronization state, preserving the distinction between
a usable snapshot and current serving health. Synchronization coordinators read
stored lifecycle state, while API readers explicitly request serving state, so
health degradation cannot itself trigger another full upstream synchronization.
Health uses the union of lifecycle rows and unprocessed inbox work. A poison
event that fails before a lifecycle row can be created is therefore still
reported as a failed, not-started resource instead of disappearing from
operational visibility.

Reconciliation age is resource-specific. Groups become stale after twice the
configured `WA_GROUP_RECONCILE_INTERVAL`; disabling periodic group
reconciliation also disables that age check. Resources without a periodic
reconciler are not assigned an arbitrary age limit. A projection capability is
advertised only while its effective state is ready and its schema version is
current. The two-minute work-lag threshold is an internal conservative default,
not a WhatsApp service limit.

Migration 16 adds a partial covering index for this aggregation. It contains
only unprocessed work and keys by instance, resource, and ingestion time, so
routine health checks do not scan the durable processed-event history.

Migration 17 adds operator actions and their audit table. Listing returns only
bounded failure metadata; normalized payloads and entity identifiers never
cross the public boundary. Replay atomically changes a dead letter back to
pending, clears failure metadata, resets its attempt budget, and appends an
audit record. Discard atomically marks the inbox row as terminal `processed`
with `discarded_at` while appending its audit record. Using the pre-existing
terminal status is intentional rollback compatibility: older binaries ignore
the row instead of mistaking a new status for permanent backlog. New binaries
distinguish successful processing from discard through `processed_at`,
`discarded_at`, and the audit action. Each audit record also retains the bounded
request identity for correlation with structured server logs.

Both actions compare against `dead_letter` inside the update transaction, so
concurrent operators cannot apply the same failure twice. The audit reason is
required and bounded, and the admin credential is never stored; only a
domain-separated reference hash is persisted. The admin-only API advertises
`projection_failure_operations` after the repository, routes, error contract,
and Swagger schema are available together.
