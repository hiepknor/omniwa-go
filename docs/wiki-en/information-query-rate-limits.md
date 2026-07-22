# WhatsApp information-query rate limits

OmniWA GO protects outbound WhatsApp information queries with a shared,
process-local guard. The guard applies to HTTP requests and internal callers,
including group/user enrichment and reconciliation work.

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

## HTTP 429 contract

Existing paths and successful response envelopes are unchanged. A guarded
request that cannot safely reach WhatsApp returns HTTP 429, a `Retry-After`
header in delta seconds, and this additive error body:

```json
{
  "error": "rate_limited",
  "code": "rate_limited",
  "retryAfter": 90
}
```

`error` remains a string for existing clients. New clients should prefer the
machine-readable `code`, wait for the larger of the header or `retryAfter`, add
jitter, and avoid automatic retries for mutations whose outcome is uncertain.

The guard is process-local. A deployment must keep one active owner for an
instance until distributed ownership and coordination are implemented.

Message/campaign pacing is a separate concern. These settings protect
information queries and do not make bulk outbound messaging safe.
