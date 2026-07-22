# WhatsApp information-query rate limits

OmniWA GO protects outbound WhatsApp information queries with a shared,
process-local guard. The guard applies to HTTP requests and internal callers,
including group/user enrichment and reconciliation work.

Ordinary group list, info, and cached invite-link reads are projection-only and
do not consume this budget. Before initial synchronization they return HTTP 503
with code `projection_not_ready`; they never fall back to a live query.

The defaults are deliberately conservative operational settings. They are not
documented or implied WhatsApp limits:

```env
WA_INFO_RATE=5/min
WA_INFO_BURST=3
WA_INFO_MAX_WAIT=5s
WA_INFO_COOLDOWN=90s
```

Each instance has an independent token bucket and circuit breaker. Identical
concurrent queries are coalesced by instance, operation, and resource. An
upstream 429 opens that instance's breaker immediately. No trial query is sent
during cooldown, and only one query is admitted when the breaker becomes
half-open.

## Identity lookup cache

`IsOnWhatsApp` results are shared by user checks and message-send preflight
within the process. The cache is isolated per instance and bounded to 10,000
entries per instance. Positive results expire after five minutes; negative
results expire after 30 seconds so a newly registered number is not hidden for
long. Logout and instance deletion remove the corresponding cache immediately.

The cache stores normalized lookup results only. It does not store tokens or
message content, and it does not replace the longer-lived contacts projection.
Expired entries remain subject to the same 10,000-entry LRU bound, are retained
for at most 90 additional seconds by default, and are used
only as a complete fallback for the read-only `/user/check` endpoint when the
query guard returns a rate-limit error. The response then remains HTTP 200 and
adds `meta: {"source":"cache","stale":true}`. Partial cache results are never
returned: when any requested identity is unavailable, the endpoint returns the
normal HTTP 429 contract. Message-send preflight never consumes stale entries.

## HTTP 429 contract

Existing paths and successful response envelopes are unchanged. A guarded
request that cannot safely reach WhatsApp returns HTTP 429, a `Retry-After`
header in delta seconds, and this additive error body:

```json
{
  "error": "rate_limited",
  "code": "rate_limited",
  "retryAfter": 90,
  "requestId": "01234567-89ab-cdef-0123-456789abcdef"
}
```

`error` remains a string for existing clients. `requestId` correlates the
response with server logs. New clients should prefer the machine-readable
`code`, wait for the larger of the header or `retryAfter`, add jitter, and avoid
automatic retries for mutations whose outcome is uncertain.

Mutations are never token-bucket limited or single-flighted by the information
query guard. If WhatsApp returns 429 from a related mutation, OmniWA GO still
opens the instance breaker and returns the same public 429 contract. Clients
must not assume that a mutation returning 429 had no effect; reconcile state or
require an explicit user retry after `Retry-After`.

The guard is process-local. A deployment must keep one active owner for an
instance until distributed ownership and coordination are implemented.

Message/campaign pacing is a separate concern. These settings protect
information queries and do not make bulk outbound messaging safe.
