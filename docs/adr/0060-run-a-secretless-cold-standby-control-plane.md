# ADR 0060: Run a secretless cold-standby control plane

- Status: Accepted
- Date: 2026-08-03

## Context

ADR 0056 selected active-passive evolution, ADR 0057 separated liveness from
readiness, ADR 0058 made the active lifecycle explicit, and ADR 0059 separated
database migration from server startup. A passive process still cannot be
deployed safely because the only server path loads the full application
configuration before opening both databases, acquiring ownership, constructing
WhatsApp clients, and starting workers.

Starting that path as a "passive" replica would either fail on the ownership
lock or accidentally create a second side-effect-capable process. The current
PostgreSQL session advisory lock prevents two cooperative application owners,
but it is not a fencing token carried by outbound WhatsApp operations. It
cannot make automatic promotion safe during every network partition.

This is an L3 lifecycle and deployment-topology change. It adds no schema,
business route, credential, or automatic failover behavior.

## Decision

The binary accepts `RUNTIME_MODE=active|standby`; an empty value remains
`active` for compatibility and every unknown value fails closed. The one-shot
`migrate` command remains independent of runtime mode.

Standby mode branches before development `.env` loading and before
`config.Load`. It creates only a process state, a minimal `net/http` server,
and three unauthenticated compatibility/control-plane routes:

- `GET /server/ok` returns the existing `200 {"status":"ok"}` contract;
- `GET /server/live` returns 200 while the control plane is alive;
- `GET /server/ready` always returns 503 because a cold standby is never active.

Every application and administrative route is absent and returns 404. The
standby receives no API key, database DSN, storage key, event-transport secret,
license configuration, migration capability, ownership connection, WhatsApp
client, or background worker. Shutdown transitions `standby -> draining ->
terminated` and closes the HTTP listener within a bounded deadline.

Runtime mode is immutable for one process. Promotion is deliberately a
stop/recreate operation: stop and verify the former active, stop the standby,
run the one-shot migration with ownership, then recreate the application in
active mode using the verified digest and full secret scope. Load balancers
must use `/server/ready`; `/server/ok` remains only a compatibility liveness
signal and must never select a traffic owner.

The production Compose standby profile publishes its control plane on host
loopback port 4001, attaches it to an isolated network, and mounts no env file,
volume, or secret. This profile is a deployment reference, not an automatic HA
controller.

## Alternatives

### Start the complete application and wait on the ownership lock

This requires all secrets and makes passive startup dependent on PostgreSQL.
Retry behavior could race the former active and provides no external-side-effect
fencing. It was rejected.

### Promote the running standby in process

This avoids process startup time but requires injecting credentials after
startup, running migrations, acquiring ownership, constructing every active
component exactly once, and fencing stale WhatsApp operations. The current lock
does not prove all of those conditions, so this was deferred.

### Make `/server/ok` fail for standby

That would improve safety for new deployments but break the compatibility
contract established by ADR 0057. Routing on `/server/ready` plus an isolated
standby port was selected instead.

### Give the standby database access for deeper health checks

Database reachability from a passive process does not prove promotion safety
and expands secret and network scope. Operator monitoring must check the
database independently.

## Consequences and risks

- A candidate image can be kept running and verified without application
  credentials or production side effects.
- Cold promotion still incurs active startup and WhatsApp reconnect time; this
  slice improves rehearsal and artifact availability, not recovery time alone.
- Any proxy that routes on `/server/ok` can send business traffic to a server
  where all such routes are 404. Enabling standby is blocked until routing on
  `/server/ready` is verified.
- A standby on the same host does not protect against host failure. A second
  failure domain requires equivalent secretless deployment and operator-owned
  routing, but does not change the process contract.
- The process exposes no build identity endpoint in standby mode. Operators
  must verify the immutable image digest and OCI revision label out of band.
- Automatic promotion remains unsafe until ownership fencing is carried across
  external side effects and split-brain drills pass.

## Acceptance, rollout, and rollback

Acceptance requires unit and race tests for strict mode parsing, single-start,
health contracts, data-plane absence, draining, and idempotent shutdown. The
container smoke test must start the image with only `RUNTIME_MODE` and
`SERVER_PORT`, observe live=200 and ready=503, prove an application route is
absent, and inspect the container environment for forbidden credential
families.

Roll out first in staging without changing traffic. Verify the active endpoint
is ready, the standby endpoint is live but not ready, the standby has no secret
mounts, and a manual stop/recreate drill meets the ADR 0056 recovery gates.
Enable the production profile only after Caddy or the orchestrator selects
backends with `/server/ready`.

Roll back by stopping the standby profile and restoring the prior immutable
application digest. Existing active startup is unchanged by the default mode;
there is no database or data rollback. Replace this decision only after a later
ADR defines fencing tokens, credential activation, promotion authorization,
split-brain containment, and tested automatic failover.
