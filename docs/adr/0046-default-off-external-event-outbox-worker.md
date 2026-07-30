# ADR 0046: Default-off external event outbox worker

- Status: Accepted
- Date: 2026-07-30

## Context

ADR 0044 added an unused transactional outbox and ADR 0045 established
confirmed transport admission. The next increment needs restart-safe worker
ownership without silently changing the current direct fan-out path. It must
also close two failure modes before any traffic is switched: a process that
repeatedly dies after claiming work must eventually exhaust a bounded attempt
budget, and ambiguous external acknowledgement must carry a stable identifier
for consumer deduplication.

Webhook destinations and per-instance RabbitMQ enablement are mutable. Storing
a destination URL would preserve the historical target but expand secret-like
configuration persistence and could send to a destination an operator has
disabled. Resolving only at enqueue time would have the same disablement
problem for delayed rows.

Core NATS flush confirms server admission but not durable persistence. It is
therefore outside the durable serving claim established by ADR 0045.

## Decision

Add a supervisor-owned, bounded outbox worker behind
`EXTERNAL_EVENT_OUTBOX_SERVE_ENABLED`, which defaults to `false`. The worker
claims a small bounded batch with the existing PostgreSQL fencing token and
lease, starts every claimed attempt concurrently so later rows cannot age out
behind earlier timeouts, applies an attempt timeout shorter than the lease,
records only allowlisted error codes,
uses capped exponential retry with stable per-delivery jitter, and leaves a
claim untouched on process shutdown for lease recovery.

An attempt is consumed when a row is claimed, before network I/O. This means a
crash or forced termination cannot retry forever without consuming its
snapshotted budget. A later claim pass dead-letters an expired or ready row
whose budget is already exhausted. Fenced completion still prevents a stale
worker from mutating a superseding claim.

Resolve current targets at delivery time:

- global Webhook uses the current global URL;
- instance Webhook selects only the current instance webhook field;
- global RabbitMQ requires the current global enablement;
- instance RabbitMQ requires the current instance enablement;
- a removed instance or disabled destination is a permanent dead letter;
- a database lookup failure is retryable.

Webhook requests carry `X-Omniwa-Delivery-ID`; RabbitMQ publications carry the
same UUID as AMQP `message_id`. Both are the stable outbox row identity. An
external 2xx or positive broker confirmation is still an at-least-once
boundary, not exactly-once consumption.

NATS rows are permanently rejected as `transport_not_supported`; the
application-layer integration must not create them. WebSocket remains direct
and best-effort.

Expose aggregate Prometheus signals for bounded outcome, transport, allowlisted
error code, attempt latency, claimed batch size, state counts, and oldest
pending age. No metric or log contains an instance ID, delivery ID, routing
key, target URL, or payload.

This increment does not create delivery rows, refactor event emission, switch
traffic, add an API or capability, or change OpenAPI.

## Alternatives

### Count only completed failures

This preserves the original repository behavior but allows repeated worker
crashes after claim to retry forever. It was rejected because the configured
attempt ceiling would not be authoritative.

### Persist the resolved webhook URL per row

This gives exact historical routing but can deliver after an operator disables
or rotates the endpoint and increases configuration exposure. It was rejected
for the current compatibility contract. A future versioned destination
snapshot would require an explicit security and operator-control decision.

### Enable serving when the binary is deployed

The table is currently empty and no atomic route builder exists. Automatic
enablement would make rollout state ambiguous and could consume manually
inserted or future-version rows. It was rejected in favor of an explicit,
default-off gate.

### Serve Core NATS rows

This would overstate server admission as durable delivery. It remains deferred
until JetStream or another durable acknowledgement contract is accepted.

## Consequences

- Worker restart, lease recovery, fencing, shutdown, retry, and dead-letter
  behavior can be exercised before production traffic changes.
- A delivery whose destination is disabled after enqueue becomes a dead letter
  rather than being sent to a stale target.
- Consumers can deduplicate ambiguous retries by delivery ID.
- Repository/state-transition infrastructure errors leave fenced rows for lease
  recovery, increment a bounded metric, and are retried on the next poll;
  transport failures remain row-level outcomes.
- Serve mode alone does not provide the end-to-end durability invariant until
  the application emitter atomically records durable history and selected
  routes.

## Rollout and rollback

1. Deploy with serve mode disabled and verify no worker is registered and
   direct delivery behavior is unchanged.
2. Exercise unit, race, PostgreSQL lease/fencing/exhaustion, real RabbitMQ
   confirmation, and webhook-header tests.
3. After the atomic emitter increment exists, enable the worker in development
   with an empty baseline and canary one durable transport at a time.
4. Alert on oldest pending age, retry rate, and any dead letter before expanding
   the canary.

Rollback first sets `EXTERNAL_EVENT_OUTBOX_SERVE_ENABLED=false`, then redeploys
the prior image. Pending rows stay in PostgreSQL for a forward fix. No migration
is added or reversed by this increment, and direct delivery remains
authoritative until the later emission cutover.
