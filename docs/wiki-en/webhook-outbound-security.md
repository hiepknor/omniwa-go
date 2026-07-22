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
Timeouts and transient delivery errors continue to use the existing retry
behavior. Response bodies and URL query strings are not written to logs.
