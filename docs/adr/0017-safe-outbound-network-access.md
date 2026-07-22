# ADR 0017: Safe outbound network access

## Status

Accepted

## Context

Media, thumbnails, profile pictures, group pictures, link previews, webhooks,
and configured converters perform outbound HTTP requests. Several paths accept
a caller-provided URL and use the default HTTP client with no private-address
policy, redirect validation, whole-request timeout, or response-size limit.
This permits server-side request forgery and unbounded resource consumption.

Media and webhooks have different legitimate network requirements, so one
hard-coded allow or deny rule is insufficient.

## Decision

Create a shared safe network client with explicit policies:

- Caller-provided media is public-network-only by default.
- Webhooks use an operator-configured allowlist. Private destinations require
  an explicit opt-in and are never enabled implicitly by a media setting.
- Configured internal dependencies use exact configured hosts.

The client validates resolved and connected IP addresses, every redirect
target, URL scheme, port policy, response status, declared length, actual body
length, and allowed content type. It uses context cancellation and bounded DNS,
connect, TLS, response-header, and whole-request timeouts. Response bodies are
read through a limit and fail when the limit is exceeded.

Request bodies and multipart uploads receive route-specific limits before Gin
or validation middleware parses them. External media processors run with a
context deadline. The production container runs as a non-root user.

The initial compatibility setting is `REMOTE_MEDIA_FETCH_POLICY` with
`disabled`, `public_only`, and `allowlist` modes. Production defaults to
`public_only`; `disabled` is the emergency rollback control.

## Observability and privacy

Metrics expose policy, result code, duration, and bounded byte counts. Logs do
not contain URL query strings, credentials, response bodies, or resolved secret
data.

## Consequences

- Some URLs that previously worked only because they reached private networks
  are rejected until explicitly allowlisted under the appropriate policy.
- All network consumers gain consistent cancellation and resource bounds.
- DNS rebinding tests require control of a test resolver or dialer.

## Rollout and rollback

Containment limits land before all callers migrate. The safe client then moves
callers by category. Operators can disable remote media fetching immediately;
they cannot bypass private-address protection through redirects.
