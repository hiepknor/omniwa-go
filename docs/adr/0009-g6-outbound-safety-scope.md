# ADR 0009: G6 outbound safety and operations scope

## Status

Accepted

## Context

Information-query protection does not make outbound messaging safe. Campaigns
amplify delivery volume and require durable control, consent, pacing, audit, and
recipient-level outcomes. Several broader administration ideas in G6 also lack
an accepted product contract.

## Decision

Introduce an independent, per-instance outbound token bucket at the service
boundary used by every WhatsApp message send. `WA_OUTBOUND_RATE` defaults to
`30/min`, `WA_OUTBOUND_BURST` to `5`, and `WA_OUTBOUND_MAX_WAIT` to `5s`.
These are conservative operator defaults, not an official WhatsApp cap, and
they do not share tokens or state with `WA_INFO_RATE`.

The public overload response will be HTTP 429 with `Retry-After` and an additive
body retaining a string `error` field. Interactive sends may wait only up to the
configured maximum. Background campaign workers must use the same guard and
persist a retry schedule instead of blocking worker goroutines for long waits.

Campaign orchestration is in scope only after this guard is enforced. It must
use durable jobs, explicit per-recipient opt-in evidence, recipient-level state,
bounded claims, pacing, pause/resume/abort, and immutable audit events. Internal
jobs are not exposed as a generic arbitrary-work API.

Webhook registration/delivery history is deferred: the current webhook
configuration and producers do not persist delivery attempts, so exposing a
history contract would be fictional. Global settings and admin-key lifecycle
are also deferred to a separate security/product proposal; G6 will not silently
expand administrative authority.

## Consequences

- Outbound load has a distinct, testable safety boundary.
- Campaign throughput can never cite the information-query limiter as proof of
  safe pacing.
- Operators must choose outbound limits appropriate to consent, provider rules,
  and their risk profile.
- Conditional G6 ideas remain explicitly out of scope until their persistence,
  authorization, retention, and product contracts are accepted.
