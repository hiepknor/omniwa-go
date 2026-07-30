# ADR 0042: Secure external event and browser-origin boundaries

- Status: Accepted
- Date: 2026-07-30

## Context

The event assembly layer added the instance bearer token to several outbound
payloads. Those payloads can leave the process through Webhook, RabbitMQ, NATS,
or WebSocket producers. Removing the known assignments alone would leave every
new event call site responsible for remembering a security rule and would not
protect nested data copied from provider events.

The HTTP router also advertised a wildcard CORS origin together with credential
support, while the WebSocket upgrader accepted every origin. Although API-key
authentication remains authoritative, the browser boundaries were inconsistent
and permitted unintended cross-origin attempts. Database startup logging also
included the complete users-database DSN at debug level.

This is an L3 security and externally observable behavior change. It is the
first deployable increment of the backend standardization program; durable
event delivery, a versioned event envelope, and credential-storage changes are
separate increments.

## Decision

Instance bearer tokens are never part of an external event contract, including
the existing compatibility contract. Remove every direct `instanceToken`
assignment from event assembly and sanitize JSON recursively at all four
external producer adapters immediately before admission or publication. The
sanitizer removes credential-shaped fields, preserves JSON number precision,
and fails closed for empty, malformed, trailing, or scalar payloads. Existing
safe event routing and identity fields remain unchanged.

Use one exact-origin policy for HTTP requests and WebSocket handshakes. Configure
cross-origin browser access with the comma-separated `HTTP_ALLOWED_ORIGINS`
environment variable. Each value must be an exact HTTP or HTTPS origin without
credentials, path, query, fragment, or wildcard. Same-host requests and clients
without an `Origin` header remain allowed. Allowed HTTP origins are reflected
without advertising `Access-Control-Allow-Credentials`; rejected origins receive
the machine-readable `origin_not_allowed` error through the shared error writer.
Invalid configured origins stop startup.

Database connection logs identify the component but never include a DSN.

No database migration, projection change, capability change, or OpenAPI schema
change is required.

## Alternatives

### Remove only the known token assignments

This fixes current payloads with less code but permits a later call site or
nested provider structure to reintroduce credentials. It was rejected because
external adapters are the reliable security boundary.

### Keep the token in the compatibility event contract temporarily

This reduces immediate consumer disruption but continues distributing a bearer
credential to systems with different retention and access controls. It was
rejected: consumers must use their configured authentication material and must
not receive instance credentials as event data.

### Keep permissive origins and rely only on API-key authentication

Authentication is still required, but this leaves inconsistent HTTP and
WebSocket browser behavior and expands the surface for credential misuse. It
was rejected in favor of one explicit policy.

### Reject requests without an Origin header

This is stricter for browsers but breaks ordinary server-to-server clients,
health checks, and command-line tools, for which `Origin` is not an
authentication signal. It was rejected; those clients remain subject to the
existing authentication middleware.

## Consequences

- Event consumers that incorrectly read `instanceToken` must stop doing so;
  there is no compatibility grace period for a leaked credential.
- Every supported event transport applies the same defense even if an assembly
  call site regresses.
- Payloads that were not valid JSON now fail instead of being delivered. Event
  producers already receive JSON from the shared event assembly path.
- Cross-origin browser clients must be listed explicitly. An empty allowlist is
  safe for same-host and non-browser clients but denies other browser origins.
- The WhatsApp Web client origin must be configured when browser pairing flows
  require it; the example configuration includes `https://web.whatsapp.com`.
- Origin checks remain a browser boundary and do not replace API-key
  authentication or cross-instance authorization.

## Rollout and rollback

Deploy first to development with every required browser origin configured.
Verify API preflight, WebSocket upgrade, rejected-origin errors, outbound event
payloads on all enabled transports, and the absence of credentials in logs.
Then promote the same immutable image through the normal release workflow.

If an origin was omitted, add it to `HTTP_ALLOWED_ORIGINS` and recreate the
service; no code or data rollback is needed. If the sanitizer rejects a valid
event shape, forward-fix the shape handling or temporarily disable the affected
transport. Rollback must never restore bearer tokens to outbound events. The
previous revision is `016a65926eef3dd889b028cf6626f23ca32c823d`; redeploying it
is not an acceptable security rollback while any external event transport is
enabled.
