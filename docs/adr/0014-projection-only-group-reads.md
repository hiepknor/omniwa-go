# ADR 0014: Projection-only group reads

## Status

Accepted; supersedes the live-read fallback in ADR 0004.

## Context

ADR 0004 retained a guarded live WhatsApp fallback while the group projection
was first deployed. That made rollout compatible, but it left ordinary list,
info, and invite-link reads capable of increasing upstream query volume before
initial synchronization or on cache misses. The mature projection now has
versioned migrations, durable state, event ingestion, reconciliation,
write-through mutations, readiness metadata, and restart coverage.

## Decision

`GET /group/list`, `POST /group/info`, and non-reset
`POST /group/invitelink` read only the persisted group projection. They never
probe WhatsApp.

If no usable current projection exists, return HTTP 503 with additive code
`projection_not_ready`. A projection is usable when it has a current schema,
a prior successful reconciliation timestamp, and status `ready`, `stale`, or
`syncing`. This preserves stale-data availability without representing an
unsynchronized database as a valid empty result.

A missing projected group or cached invite link returns HTTP 404 with code
`not_found`. Invite-link reset remains a live mutation. It is not
single-flighted or charged to the information-query token bucket; an upstream
429 still opens the shared breaker, and a successful reset writes the returned
link through to the projection.

## Consequences

- Refreshing group views cannot increase WhatsApp information-query traffic.
- Startup and initial-sync gaps are explicit 503 responses instead of false
  empty data or hidden live calls.
- A cache miss no longer fetches an invite link implicitly; clients can ask a
  user to perform the explicit reset mutation when appropriate.
- The shared query guard remains required for reconciliation, enrichment, and
  other live information queries.
