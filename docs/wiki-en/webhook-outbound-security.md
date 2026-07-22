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
WEBHOOK_WORKERS=4
WEBHOOK_QUEUE_CAPACITY=256
WEBHOOK_MAX_PENDING_PER_INSTANCE=32
WEBHOOK_MAX_ATTEMPTS=3
WEBHOOK_RETRY_BASE=1s
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

## Bounded delivery behavior

Webhook work is admitted to an in-memory queue and processed by a fixed worker
pool. `WEBHOOK_QUEUE_CAPACITY` bounds total outstanding work, including active
requests and queued deliveries. `WEBHOOK_MAX_PENDING_PER_INSTANCE` prevents one
instance from consuming the entire process budget. Queue saturation is returned
to the internal caller and recorded with a safe structured error code. Accepted
HTTP deliveries are processed only by the fixed worker pool, never by a new
goroutine per delivery.

The queue intentionally is not a durable delivery ledger. A process restart or
shutdown can abandon accepted work, and OmniWA GO does not expose webhook
delivery history. Consumers requiring guaranteed delivery must use a configured
durable broker and implement their own acknowledgement and replay policy.
