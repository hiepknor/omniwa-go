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
