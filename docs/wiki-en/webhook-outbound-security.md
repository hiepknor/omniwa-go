# Webhook outbound network policy

OmniWA GO sends webhooks through the shared bounded outbound HTTP client. A
target must match both an exact configured host and an allowed port. Redirects
are checked again and cannot escape that policy.

The host in `WEBHOOK_URL` is added automatically. Hosts used by per-instance
webhooks must be listed explicitly:

```env
WEBHOOK_ALLOWED_HOSTS=hooks.example.com,backup-hooks.example.com
WEBHOOK_ALLOWED_PORTS=443
WEBHOOK_ALLOW_PRIVATE=false
WEBHOOK_TIMEOUT=10s
WEBHOOK_MAX_REQUEST_BYTES=4194304
WEBHOOK_MAX_RESPONSE_BYTES=65536
```

Host entries do not contain a scheme, path, credentials, query, or port. Ports
are configured separately. Private, loopback, link-local, and cloud metadata
addresses are blocked by default, including after DNS resolution and on every
redirect.

Set `WEBHOOK_ALLOW_PRIVATE=true` only when an allowlisted webhook intentionally
runs on a private network. This switch does not affect remote media fetching or
any other outbound category.

Requests that violate the network policy fail permanently and are not retried.
HTTP 408, 425, 429, 5xx responses, and transient network failures are retried
with bounded exponential backoff. Other 4xx responses, unsafe targets,
oversized responses, and cancellation are permanent failures. Response bodies
and URL query strings are not written to logs.

## Durable delivery behavior

Webhook routes are atomically recorded with durable event history and processed
by the PostgreSQL outbox worker. Worker batch size, leases, attempt timeout, and
bounded exponential retry are configured with `EXTERNAL_EVENT_OUTBOX_*`.
Restarted workers reclaim expired leases. Every request carries a stable
`X-Omniwa-Delivery-ID`; receivers should use it for idempotency because an
ambiguous network acknowledgement can result in at-least-once delivery.

## `SendMessage` phone-number contract

When both `WA_PHONE_IDENTITY_EVIDENCE_ENABLED` and
`WA_PHONE_NUMBER_EXPOSURE_ENABLED` are `true`, an outbound `SendMessage`
payload may contain `data.recipientPhoneNumber`:

```json
{
  "event": "SendMessage",
  "data": {
    "recipientPhoneNumber": "<digits-only phone number>"
  }
}
```

The field is additive and optional. It has no leading plus sign and is not a
claim that the value is valid E.164. Consumers must continue to use the
provider JID and message identifiers for identity and deduplication. They must
not use the phone number as a database key, log label, or delivery identifier.

For an acknowledgement that contains only a LID, the field is emitted only
when the current send operation resolved an explicitly requested phone JID to
that acknowledged LID. The application never consults the provider-global LID
map for public exposure. Disabling `WA_PHONE_NUMBER_EXPOSURE_ENABLED` and
restarting removes phone-number fields at the transport boundary, including
from queued deliveries that have not yet left the application.

Phone numbers are personal data. A receiver must apply its access, retention,
deletion, encryption, and incident-response controls before this field is
enabled. Do not write raw payloads or phone numbers to application logs.

## Production canary rollout

The real receiver hostname is an operator-supplied activation input. Do not
commit it to this repository unless it is intentionally public configuration.
Use an immutable image digest and retain the previous digest before starting.

1. Configure the receiver with HTTPS, a valid certificate, a bounded request
   body, fast 2xx acknowledgement, and idempotency keyed by
   `X-Omniwa-Delivery-ID`.
2. Deploy with `WEBHOOK_ALLOWED_HOSTS` set to that exact hostname,
   `WEBHOOK_ALLOWED_PORTS=443`, and `WEBHOOK_ALLOW_PRIVATE=false`. Do not include
   a scheme, path, wildcard, credential, query, or port in the host allowlist.
3. Enable `WA_PHONE_IDENTITY_EVIDENCE_ENABLED=true` while keeping
   `WA_PHONE_NUMBER_EXPOSURE_ENABLED=false`. Restart and confirm evidence
   failure/conflict metrics remain at baseline.
4. Select one consented instance. Record its complete current subscription and
   transport configuration before changing it. `POST /instance/connect`
   replaces the subscription set and the Webhook, RabbitMQ, NATS, and WebSocket
   settings; activation must resend the preserved values while adding
   `SEND_MESSAGE` and the HTTPS Webhook URL.
5. Send exactly one authorized canary message. Confirm one durable Webhook row,
   a receiver 2xx, a stable delivery ID, and an optional digits-only
   `recipientPhoneNumber` equal to the authorized target. Do not print the
   target, JID, delivery ID, message ID, token, or payload during validation.
6. Observe the canary for at least 24 hours before expanding. Stop on any
   cross-instance mismatch, unexpected retention, payload-policy failure,
   growing pending age, retry burst, or dead letter.

Useful aggregate-only Prometheus signals are:

```promql
omniwa_external_event_outbox_rows{status=~"pending|processing|dead_letter"}
omniwa_external_event_outbox_oldest_pending_age_seconds
sum by (outcome, code) (rate(omniwa_external_event_outbox_attempts_total{transport="webhook"}[5m]))
sum by (outcome) (rate(omniwa_phone_identity_payload_policy_total[5m]))
sum by (outcome) (rate(omniwa_phone_identity_evidence_total[5m]))
```

These metrics contain no instance, destination, delivery ID, JID, or phone
number labels.

## Rollback

1. Set `WA_PHONE_NUMBER_EXPOSURE_ENABLED=false` and restart first. This is the
   privacy kill switch and redacts still-queued phone fields.
2. Restore the canary instance's complete captured subscription and transport
   configuration, removing the Webhook URL and `SEND_MESSAGE` only if they were
   introduced by this rollout. Do not send a partial `/instance/connect`
   request because omitted transport settings are replaced.
3. Confirm Webhook pending and processing rows drain. Investigate or explicitly
   operate dead letters; never delete outbox rows as a rollback shortcut.
4. Remove the receiver hostname from `WEBHOOK_ALLOWED_HOSTS` and restart.
   `WA_PHONE_IDENTITY_EVIDENCE_ENABLED` may remain enabled for diagnosis or be
   disabled in a second restart.
5. If application rollback is required, deploy the retained immutable digest
   only after the outbox compatibility checks in ADR 0048 are satisfied.

Already delivered personal data cannot be recalled by the application. Follow
the receiver's deletion and incident-response procedure when rollback is caused
by incorrect disclosure.
