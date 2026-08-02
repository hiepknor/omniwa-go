# ADR 0057: Separate process liveness and readiness

- Status: Accepted
- Date: 2026-08-02

## Context

ADR 0056 selected an active-passive modular-monolith direction. The current
`/server/ok` endpoint returns success whenever the fully initialized router is
serving. It cannot represent a future standby process that is alive but must
not receive business traffic, and it does not make the transition to draining
observable before shutdown.

Database ownership, process liveness, traffic readiness, and projection health
are different signals. Treating any one of them as all four can route requests
to a standby or stale owner. PostgreSQL advisory ownership also does not fence
external WhatsApp side effects during a network partition.

## Decision

The process has a bounded, race-safe role state machine:

```text
starting -> active -> draining -> terminated
starting -> standby -> promotion_pending -> active
```

Standby and promotion transitions are foundation only in this change; current
single-replica startup still follows the first path. Invalid transitions fail
closed.

Two additive unauthenticated endpoints expose no dependency, topology, or
credential detail:

- `/server/live` returns 200 while the process control plane is alive and 503
  after termination.
- `/server/ready` returns 200 only in the active role and 503 otherwise.

`/server/ok` retains its existing response and semantics for compatibility.
Caddy and orchestrators must not switch to `/server/ready` until the endpoint
has been deployed and verified. Projection and instance health remain on their
existing authenticated endpoints and do not determine process readiness in
this foundation slice.

Prometheus exports only fixed role and transition labels. State observations
carry a monotonic process-local revision so delayed concurrent observations
cannot overwrite a newer gauge state.

## Alternatives

### Change `/server/ok` into readiness

This would silently change existing health checks and could restart or remove a
healthy process during rollout. Additive endpoints were selected instead.

### Use database ownership as the only readiness signal

The database session can be lost before the old process detects the failure,
and ownership says nothing about completion of runtime startup or draining.

### Expose detailed dependency failures publicly

That would disclose operational topology and create an unstable public
contract. Detailed health remains authenticated and metrics remain admin-only.

## Consequences

- Current deployments remain compatible.
- Future standby processes can remain live without receiving traffic.
- The role state is necessary but not sufficient for safe promotion; external
  fencing is still mandatory.
- Startup and shutdown must transition the process state at explicit lifecycle
  boundaries.

## Rollout and rollback

Deploy with existing `/server/ok` checks unchanged. Verify both new endpoints
and bounded metrics, then update staging health checks before production. Revert
the change to roll back; there is no schema or persistent-state mutation.
