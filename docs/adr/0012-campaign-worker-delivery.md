# ADR 0012: Campaign worker delivery and retry policy

## Status

Accepted

## Context

Durable recipient leases do not themselves deliver messages. The runtime needs
a bounded worker that preserves per-instance outbound pacing, survives process
restarts, distinguishes capacity deferrals from failed attempts, and minimizes
duplicate sends across the database-to-provider boundary.

## Decision

Run a background worker that claims a configurable batch of ready recipients
through the campaign repository. It loads each instance-scoped campaign and
sends text through the existing send service. This keeps every worker send
behind the shared per-instance `WA_OUTBOUND_*` guard and the normal projection
write-through path; the worker must not call the WhatsApp client directly.

Each recipient uses a deterministic provider message ID derived from its stable
recipient-job UUID. A restarted worker therefore reuses the same identity. This
reduces duplicate risk but does not claim exactly-once delivery across an
external system.

Local outbound and information-query rate-limit errors schedule a deferral at
the supplied retry time without consuming the recipient's attempt budget.
Pre-send database dependency failures are also deferred without consuming an
attempt; this prevents infrastructure outages from exhausting delivery
budgets before a provider send is attempted. Other errors use capped
exponential backoff and consume an attempt. Once
`WA_CAMPAIGN_MAX_ATTEMPTS` is reached, the recipient becomes failed. Only
bounded error codes are persisted; upstream error text, message content,
recipient addresses, tokens, and consent evidence are not logged.

The worker attempts campaign completion after processing an affected batch.
Repository transition rules remain authoritative: a campaign cannot complete
while pending or processing recipients remain. Pause and abort prevent new
claims, while already leased work retains the in-flight boundary defined by
ADR 0011.

Defaults are operational controls, not official WhatsApp platform caps:

```env
WA_CAMPAIGN_BATCH=10
WA_CAMPAIGN_LEASE=2m
WA_CAMPAIGN_POLL_INTERVAL=1s
WA_CAMPAIGN_MAX_ATTEMPTS=3
WA_CAMPAIGN_RETRY_BASE=30s
```

## Consequences

- Multiple replicas can safely process disjoint batches using database leases.
- Capacity pressure delays work without turning throttling into a delivery
  failure.
- Generic failures are bounded and auditable instead of retrying forever.
- Crash recovery is at-least-once; deterministic message IDs and downstream
  reconciliation mitigate, but cannot eliminate, duplicates.
- Public campaign lifecycle APIs and capability advertisement remain a separate
  additive contract.
