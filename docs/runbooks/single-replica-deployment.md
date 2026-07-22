# Single-replica deployment

## Scope

OmniWA GO currently supports exactly one application replica for each users
database. PostgreSQL-backed projections and campaign jobs can coordinate work,
but WhatsApp socket ownership, in-process rate guards, and realtime fan-out do
not yet have distributed owner fencing. Do not configure active-active replicas
until the instance-owner lease design in ADR 0016 is implemented.

## Enforced startup invariant

At startup, the application reserves a dedicated PostgreSQL connection and
acquires a database-scoped advisory lock. A second process pointed at the same
users database exits before migrations, background workers, HTTP listeners, or
WhatsApp connections start. The expected error contains:

```text
another OmniWA GO application replica already owns this users database
```

The lock connection is checked every five seconds. If that session is lost, the
application initiates graceful shutdown because PostgreSQL has already released
the advisory lock. This is containment for an accidental duplicate deployment;
it is not a substitute for a distributed per-instance lease.

## Deployment settings

- Docker Compose: run one `omniwa-go` service container. Do not use `--scale`.
- Docker Swarm: set `deploy.replicas: 1` and `update_config.order: stop-first`.
- Kubernetes: set `replicas: 1`, use the `Recreate` strategy, and do not attach
  a HorizontalPodAutoscaler.
- Use a shared, durable `POSTGRES_USERS_DB`. The lock boundary follows that
  database; using a different database creates an independent deployment.

Stop-first/Recreate upgrades intentionally have a short outage. Start-first or
surge rollouts cause the replacement to exit while the current process owns the
lock, which can create a restart loop and a misleading failed rollout.

## Verification

1. Confirm only one workload is configured and ready.
2. Start a second copy with the same `POSTGRES_USERS_DB` and verify that it exits
   with the ownership error before binding the API port.
3. Stop the first copy, then verify a new copy acquires ownership and starts.
4. Confirm logs contain `component=ownership action=acquire result=success`.
5. Confirm instance reconnects and `/server/ok` succeeds after the replacement.

## Rollback

Rollback uses the same stop-first sequence: stop the current application, deploy
the previous immutable image digest, and verify ownership acquisition and health.
Never bypass the lock to restore a multi-replica topology.
