# Outbound message rate limits

OmniWA GO applies one shared token bucket per instance before every outbound
WhatsApp message send. The guard is enforced at the service boundary, including
internal sends such as call-rejection messages, so bypassing an HTTP handler
does not bypass pacing. It is independent from the information-query guard.

Configure it with:

```env
WA_OUTBOUND_RATE=30/min
WA_OUTBOUND_BURST=5
WA_OUTBOUND_MAX_WAIT=5s
```

These values are conservative, operator-configurable defaults. They are not an
official WhatsApp platform cap. Operators remain responsible for consent,
provider policy, content, and choosing a rate appropriate to their workload.

`WA_OUTBOUND_RATE` accepts a positive count per second, minute, or hour, such as
`1/s`, `30/min`, or `300/hour`. `WA_OUTBOUND_BURST` is the maximum immediately
available capacity. `WA_OUTBOUND_MAX_WAIT` bounds how long an interactive call
may wait for a token.

When the required delay exceeds that bound, the API returns HTTP 429:

```http
Retry-After: 2
Content-Type: application/json
```

```json
{
  "error": "outbound_rate_limited",
  "code": "outbound_rate_limited",
  "retryAfter": 2
}
```

`error` remains a string. `code` and `retryAfter` are additive fields. Clients
should wait at least the `Retry-After` delta-seconds before retrying and should
not retry in a tight loop.

Campaign workers use this same guard, but the limiter alone does not make a
campaign safe. Durable scheduling, per-recipient opt-in evidence, lifecycle
controls, and audit records are separate requirements.

The durable worker can be tuned independently:

```env
WA_CAMPAIGN_BATCH=10
WA_CAMPAIGN_LEASE=2m
WA_CAMPAIGN_POLL_INTERVAL=1s
WA_CAMPAIGN_MAX_ATTEMPTS=3
WA_CAMPAIGN_RETRY_BASE=30s
```

Rate-limit and pre-send dependency deferrals do not consume the delivery
attempt budget. Provider send failures use capped exponential backoff. These
settings coordinate worker execution and are not WhatsApp platform limits.

Clients can detect this contract through the additive `outbound_rate_limit`
value returned by `GET /server/capabilities`.
