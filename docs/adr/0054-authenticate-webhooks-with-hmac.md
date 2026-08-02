# ADR 0054: Authenticate operator-owned webhooks with HMAC-SHA-256

- Status: Accepted
- Date: 2026-08-02

## Context

Webhook delivery used HTTPS and an outbound destination allowlist, but a
receiver had no cryptographic proof that a request was emitted by OmniWA GO.
An attacker able to reach the receiver could submit a payload with a fabricated
delivery ID. TLS authenticates the receiver to the sender; it does not
authenticate the sender at the application boundary.

Webhook delivery is at least once. A stable delivery ID is reused across
attempts, while attempts may occur long after the event was first recorded.
The signing protocol therefore needs to bind the exact transmitted bytes and
the stable delivery identity while issuing a fresh replay-window timestamp for
each attempt.

This is an L3 security and external integration contract. Existing receivers
must continue to work while operators stage verification, and rollback must
not require changing durable outbox rows.

## Decision

OmniWA GO supports optional deployment-wide HMAC-SHA-256 signatures for
operator-owned Webhook receivers. When `WEBHOOK_SIGNATURE_ENABLED=true`, a
standard-base64 secret that decodes to exactly 32 bytes and a safe key ID are
required. Startup fails when enabled signing material is missing or invalid.
The secret supports the standard `WEBHOOK_SIGNATURE_SECRET_FILE` boundary.

The sender sanitizes the JSON payload first, then signs the exact bytes passed
to the HTTP client. Every signed request carries:

- `X-Omniwa-Delivery-ID`: the existing stable UUID used for deduplication;
- `X-Omniwa-Signature-Timestamp`: Unix seconds generated for this attempt;
- `X-Omniwa-Signature-Key-ID`: the configured non-secret rotation identifier;
- `X-Omniwa-Signature`: `v1=` followed by a lowercase hexadecimal HMAC.

The version 1 canonical byte sequence is:

```text
<timestamp>.<delivery-id>.<raw-request-body>
```

Receivers select the secret by key ID, recompute the HMAC over the unmodified
request body, and compare signatures in constant time. They reject malformed
or missing headers and timestamps outside their configured clock-skew window.
Previously accepted delivery IDs are idempotent no-ops that return 2xx without
reprocessing; returning a permanent error for a successfully handled delivery
would violate the at-least-once acknowledgement contract. A five-minute
timestamp window is the recommended default. Delivery-ID deduplication remains
required because a legitimate retry receives a fresh timestamp and signature.

Signing is disabled by default so a new binary is deployable before receivers
understand the headers. Production rollout is receiver-first: accept and
observe valid signatures, enable signing on a canary sender, then require
signatures at the receiver before expanding. The feature flag is a migration
control, not an acceptable permanent production setting for protected
receivers.

The signing secret is scoped to a deployment and trusted operator-owned
receivers. It is not an instance or tenant credential and must not be shared
with mutually untrusted per-instance receivers. Such deployments require a
future per-destination key-management contract rather than distributing this
deployment key.

## Alternatives

### Sign only the request body

This proves payload integrity but does not bind the delivery identity or a
freshness signal. An intercepted valid request could be replayed with altered
metadata. Binding timestamp and delivery ID was selected.

### Reuse an instance API token

Giving an HTTP receiver an instance bearer token would grant unrelated API
authority and couple Webhook authentication to token rotation. It was
rejected.

### Use one secret per instance in the database

This provides stronger tenant isolation but requires a new encrypted secret
lifecycle, provisioning and rotation APIs, database migration, and recovery
contract. It should be designed separately before supporting mutually
untrusted tenant receivers. The current deployment key is deliberately
restricted to operator-owned receivers.

### Make signatures mandatory immediately

That would convert queued deliveries into retries or dead letters until every
receiver is upgraded. The additive feature flag supports a safe receiver-first
rollout.

## Consequences

- Signed requests authenticate the exact bytes, timestamp, and delivery UUID.
- Payload sanitization remains the transport boundary and occurs before HMAC.
- Secrets are neither included in payloads nor written to logs.
- Clock synchronization and bounded replay storage become receiver
  responsibilities.
- Rotation deploys receiver support for both key IDs first, changes the sender
  key and key ID, observes the old retry horizon, then removes the old key.
- A receiver that permanently accepts unsigned requests remains vulnerable to
  downgrade; signature enforcement is required after rollout.
- No database migration or durable outbox rewrite is required.

## Rollout and rollback

Deploy the binary with signing disabled and verify unchanged delivery. Add the
new key to the receiver, where signed requests are verified but unsigned
requests are temporarily observed. Configure the sender secret through a
restricted file, set a unique key ID, enable signing on staging, and verify a
canary including body-tamper and stale-timestamp rejection plus duplicate
delivery no-op behavior. Promote the same immutable image and signing configuration to
production, then require signatures at the receiver.

Rollback first permits unsigned requests at the receiver, then sets
`WEBHOOK_SIGNATURE_ENABLED=false` and recreates the sender. Existing outbox
rows remain compatible and must not be deleted. Retain the previous immutable
image digest and signing key until the maximum retry horizon has elapsed.
