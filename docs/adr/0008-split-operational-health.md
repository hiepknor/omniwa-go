# ADR 0008: Split operational health

## Status

Accepted

## Context

A single healthy/unhealthy flag hides materially different conditions. The API
can serve persisted data while a WhatsApp instance is disconnected, one
projection is still syncing, or information queries are cooling down after an
upstream throttle.

## Decision

Keep unauthenticated `GET /server/ok` as process/API liveness. Add authenticated
`GET /server/health` with independent dimensions for each visible instance:

- `connection` uses the persisted instance connection flag and never probes
  WhatsApp;
- `projection` combines persisted per-resource sync state with durable inbox
  backlog age, processing/failure counts, unresolved dead letters, and
  resource-specific reconciliation age; it is `not_started` when no resource
  state exists;
- `throttling` reports the in-memory per-instance information-query circuit,
  its observation state, cooldown deadline, and retry interval;
- `api` confirms that the authenticated health request was served.

Instance tokens see only their instance. Admin keys see all instances. The
endpoint does not collapse these dimensions into one overall status, and it
does not claim that an unobserved circuit proves upstream availability.

## Consequences

- Operators can distinguish API, connection, projection, and throttling faults.
- Health refreshes do not consume WhatsApp query capacity.
- Circuit state is process-local and resets on restart; persisted projection and
  connection states survive restart.
- The endpoint contains operational metadata only and never returns tokens,
  message content, contact data, or provider payloads.
- Derived health preserves the stored status as additive diagnostic metadata
  when it differs, so operators can distinguish snapshot lifecycle from current
  serving readiness without inspecting event payloads.
