# External event reliability contract

This document records the current engineering contract for external event
delivery. It is not a customer-facing service-level agreement.

## Delivery matrix

| Transport | Admission boundary | Guarantee after admission | Consumer requirement |
|---|---|---|---|
| Webhook | Durable event and delivery rows commit in PostgreSQL | At least once; bounded retries can end in dead letter | Deduplicate by `X-Omniwa-Delivery-ID` and verify HMAC when enabled |
| RabbitMQ | Durable event and delivery rows commit in PostgreSQL | At least once after broker confirmation; bounded retries can end in dead letter | Deduplicate by the stable delivery ID |
| NATS | The direct producer accepts the publish attempt | Best effort; no PostgreSQL outbox replay | Do not treat receipt as a durable audit record |
| WebSocket | An in-process connection is live | Ephemeral; disconnects and restarts can lose events | Resynchronize through projection-backed APIs |

Durable history and transport delivery are separate concepts. A durable event
can exist with no external route, and a successful API response does not imply
that every configured external consumer has processed the event.

## Failure and retry rules

- Webhook and RabbitMQ deliveries are claimed from PostgreSQL with leases.
- A failed retryable attempt is rescheduled with bounded exponential backoff and
  stable jitter.
- A non-retryable failure or exhausted attempt budget becomes a dead letter.
- Process shutdown or failover must leave committed rows replayable by the next
  active owner.
- Consumers must assume duplicates around timeouts, broker confirmations, and
  process failure. Exactly-once delivery is not promised.
- Operators must not repair incidents by deleting or rewriting outbox rows.

## Operational signals

The active process must expose liveness independently from transport health.
Operators monitor pending, processing, and dead-letter counts, oldest pending
age, attempt outcomes, and repository failures. A failover drill is successful
only when the new owner acquires exclusive ownership and the durable backlog
resumes without manual database changes.

## Deferred decisions

NATS durability, WebSocket replay, per-tenant delivery credentials, and
multi-active routing require separate ADRs. Until then, code and documentation
must not describe NATS or WebSocket as durable.
