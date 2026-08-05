# ADR 0062: Gate readiness on stabilized dependency health

- Status: Accepted
- Date: 2026-08-05

## Context

`/server/ready` historically reported only whether the process held the active
runtime role. A process could therefore remain ready while a dependency was
unavailable. Making every dependency mandatory immediately would also be risky:
optional RabbitMQ and MinIO failures must not remove healthy API replicas, and a
single transient probe failure must not cause load-balancer flapping.

## Decision

Readiness remains backward compatible by default. Operators may opt in to these
bounded requirement groups:

- `READINESS_REQUIRE_USERS_DATABASE`
- `READINESS_REQUIRE_EVENT_DELIVERY`
- `READINESS_REQUIRE_MINIO`

A required dependency becomes healthy after two consecutive successful probes,
becomes unhealthy after three consecutive failed probes, and is unhealthy when
its latest observation is older than 45 seconds. Event delivery requires the
durable outbox and RabbitMQ when RabbitMQ is configured. MinIO requires each
configured media store. Process-role readiness remains an unconditional gate.

## Rollout and rollback

Roll out one group at a time after observing healthy probes for a normal traffic
cycle. Start with the users database, then event delivery, then MinIO where media
availability is part of the serving contract. Roll back by setting the affected
flag to `false` and redeploying; no database or API migration is involved.

## Consequences

Opted-in deployments stop receiving traffic during sustained or stale required
dependency failures. Recovery has a bounded delay that prevents oscillation.
Defaults deliberately remain permissive until operators complete staged rollout.
