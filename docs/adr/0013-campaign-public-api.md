# ADR 0013: Instance-scoped campaign orchestration API

## Status

Accepted

## Context

The durable campaign store and worker need an additive public contract for the
console and other instance clients. Exposing repository rows directly or
unbounded recipient histories would leak internal coordination fields, create
large responses, and make future schema evolution unsafe.

## Decision

Expose campaign orchestration under `/campaigns`, protected by the existing
instance-token middleware. Admin keys are not accepted on these routes because
every campaign, recipient, transition, and audit event must have one explicit
instance scope. The instance token is hashed with the campaign scope before it
is stored as actor attribution; raw keys and consent references are never
persisted or returned.

The API is additive and uses the existing `{ "message": "success", "data":
... }` envelope. Lists use bounded keyset pagination with opaque, versioned,
resource-typed cursors. A cursor cannot be replayed against another list type.
Requests are bounded to 8 MiB and draft creation remains capped at 10,000
recipients.

The lifecycle is strict:

```text
draft -> scheduled -> running <-> paused
  |          |           |
  +----------+-----------+-> aborted
```

`schedule` persists `startsAt` on the campaign and recipient due times. `start`
arms delivery; the worker still waits until each recipient is due. Pause and
abort prevent new claims. Work leased before either transition may finish, as
defined by ADR 0011. Terminal campaigns cannot be restarted.

Every recipient requires a direct WhatsApp JID, opt-in source, evidence
reference, and opt-in timestamp. The API accepts the evidence reference only
to create its scoped hash. It returns normalized recipient state and safe error
codes, never claim tokens, leases, evidence hashes, or actor hashes.

Known validation errors return HTTP 400, missing instance-scoped campaigns
return 404, and invalid/concurrent lifecycle changes return 409. Unexpected
errors return a generic 500 without database or provider details.

Advertise `campaign_orchestration` through `/server/capabilities` once these
routes, durable storage, and the worker are all available.

## Consequences

- Console clients can create, inspect, control, and audit campaigns without
  direct access to internal job tables.
- Stable pagination bounds database and response work for large campaigns.
- Consent and actor evidence remain useful for audit without storing raw
  secrets.
- Campaign scheduling requires an explicit `start`; this makes activation an
  auditable operator decision and avoids a hidden scheduler transition.
- The API currently supports text campaigns only. New content types require an
  additive request contract and worker implementation.
