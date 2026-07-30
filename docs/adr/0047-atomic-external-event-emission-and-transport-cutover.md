# ADR 0047: Atomic external event emission and transport cutover

- Status: Accepted
- Date: 2026-07-30

## Context

ADR 0044 added the transactional outbox schema, ADR 0045 established confirmed
transport admission, and ADR 0046 added a default-off worker. Durable history
was still written separately before callback code launched instance and global
fan-out goroutines. Subscription selection lived in `CallWebhook`, instance
transport selection lived in `sendToQueueOrWebhook`, and global RabbitMQ/NATS
selection lived in `SendToGlobalQueues`. Enabling the worker without replacing
that split boundary would either create duplicate deliveries or retain the
database-to-transport crash window.

Recording shadow delivery rows while direct delivery remains authoritative is
not safe. Those rows would later be indistinguishable from unsent work and
serving them would replay events already sent directly.

## Decision

Introduce one application emitter that normalizes the retention-bound durable
history record, computes selected Webhook and RabbitMQ routes, and records the
history row plus all selected routes in one PostgreSQL transaction. Event
payload marshaling completes before this boundary. A marshal or transaction
failure therefore creates neither a history claim nor a partial route set.

Extract deterministic route decisions for:

- instance subscription matching, including group/newsletter compatibility;
- global RabbitMQ specific-event priority and legacy event-group fallback;
- instance/global Webhook destinations;
- instance/global RabbitMQ destinations.

The emitter is always used for durable history. Its durable transport set is
configured by `EXTERNAL_EVENT_OUTBOX_EMIT_TRANSPORTS`, which defaults to empty
and accepts only `webhook` and `rabbitmq`. An empty set records no delivery rows
and preserves direct fan-out. Selecting a transport atomically creates its
routes and suppresses that transport in both instance and global direct paths.
The same deployment must enable `EXTERNAL_EVENT_OUTBOX_SERVE_ENABLED`; startup
rejects durable emission without a serving worker.

WebSocket and Core NATS remain direct. NATS cannot be selected because Core
NATS does not provide the durable acknowledgement required by this contract.
Destination URLs and credentials are still resolved at delivery time and are
not copied into routing rows.

The compatibility `CallWebhook` and `SendToGlobalQueues` entry points remain
during rollout for upstream stability, but event-producing call sites use the
application emitter. Their selected durable transports act only as adapters:
they cannot send the same transport directly.

## Alternatives

### Record shadow rows and keep every direct transport

This appears easy to compare operationally but creates an unsafe replay set and
guaranteed duplicates when serve mode is enabled. It was rejected.

### Switch every transport in one deployment

This minimizes temporary branching but combines Webhook and RabbitMQ failure
domains and makes rollback ambiguous. It was rejected in favor of one-transport
canaries.

### Persist resolved destination URLs

This would freeze historical routing but expands sensitive configuration at
rest and can ignore a later operator disablement. ADR 0046's current-target
resolution remains authoritative.

## Consequences

- Durable history and selected external routes have one atomic acceptance
  boundary.
- A selected transport is at-least-once and carries a stable delivery UUID;
  consumers remain responsible for deduplication.
- Direct behavior remains unchanged while the emit transport set is empty.
- Subscription and global RabbitMQ mapping become testable without network I/O.
- Bounded emitter metrics report atomic record outcomes, planned route counts,
  and successfully accepted routes by transport and destination. They never use
  instance IDs, routing keys, destinations, delivery IDs, or payloads as labels.
- Payloads for selected routes are retained under the existing outbox and
  durable-event retention boundary; no new table or migration is required.
- A failed atomic write suppresses external dispatch instead of claiming an
  event that cannot be recovered.

## Rollout and rollback

1. Deploy with serve disabled and an empty emit transport set. Verify the
   emitter records durable history with zero outbox rows and direct fan-out is
   unchanged.
2. Enable serve mode with the emit set still empty. Verify worker health and an
   empty baseline.
3. Canary `webhook` on development. Confirm direct Webhook suppression, stable
   delivery IDs, queue age, retries, and zero unexpected dead letters.
4. Remove `webhook` from the emit set to roll back its cutover while leaving
   serve mode enabled long enough to drain accepted rows. Disable serve only
   after no pending/processing rows remain.
5. Repeat independently for `rabbitmq` after consumer deduplication readiness.

Application rollback must first clear the emit transport set. A previous image
must not be deployed while new pending rows exist unless the current worker is
left running to drain them. No database rollback or destructive cleanup is
required, and migration history remains unchanged.
