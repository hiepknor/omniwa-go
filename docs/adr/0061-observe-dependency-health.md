# ADR 0061: Observe dependency health without changing readiness

## Context

Process readiness currently represents active traffic ownership. It does not
show whether PostgreSQL, the external-event outbox, RabbitMQ, or private media
storage is reachable after startup. Operators therefore need separate cached
dependency observations before a later rollout can safely decide which failures
should remove a process from traffic.

Calling dependencies from `/server/health` or `/server/ready` would put network
latency and failure amplification on the HTTP control plane. Making every
optional transport a readiness requirement would also allow a media or broker
outage to remove otherwise useful API capacity.

## Decision

Add an observe-only dependency health registry under `pkg/server/service`.
Bounded background workers probe configured dependencies every 15 seconds with
a five-second timeout and publish immutable cached snapshots. The authenticated
`/server/health` response includes the additive `dependencies` array, and
Prometheus exposes one-hot gauges with allowlisted dependency and status labels.

Initial dependencies are the users database, external-event outbox, configured
RabbitMQ, legacy media bucket, shared media-assets bucket, and campaign-media
storage. Raw errors, endpoints, bucket names, instance identities, and
credentials are never stored or exposed; failures use only `probe_failed` or
`probe_timeout`.

This change deliberately does not affect `/server/ready`, process roles,
restart behavior, or traffic routing. Hard/soft readiness policy requires a
separate staged decision after observation data is available.

## Alternatives

- Probe dependencies synchronously from readiness: rejected because it couples
  control-plane latency to downstream networks and can cause cascading outage.
- Fail readiness for every configured dependency: rejected because RabbitMQ and
  media are optional for substantial parts of the API.
- Expose raw provider errors: rejected because they can contain sensitive
  infrastructure details and create unbounded metric labels.
- Rely only on request failures: rejected because this detects outages late and
  gives no stable operator signal during idle periods.

## Consequences

- Health is eventually consistent by at most one probe interval plus timeout.
- Startup snapshots begin as `unknown` until the first check completes.
- RabbitMQ health may reconnect the existing producer, but does not publish or
  declare a queue.
- MinIO health verifies authenticated bucket visibility but is not a full
  write/read/delete synthetic test; operators retain the production preflight
  probe for that guarantee.
- Additional low-frequency dependency traffic is introduced and bounded to one
  worker per enabled dependency.

## Rollout and rollback

Roll out as observation-only and monitor gauge stability for at least 24 hours.
No alert in this phase should automatically restart a process or remove it from
traffic. Rollback by reverting the application image; no schema or external
state changes are involved. The next readiness-policy phase must remain behind
explicit configuration and cannot infer criticality solely from feature enablement.
