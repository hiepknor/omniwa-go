# ADR 0001: Shared WhatsApp Information Query Guard

- Status: Accepted
- Date: 2026-07-22

## Context

OmniWA GO currently issues WhatsApp information queries directly from multiple
domain services. Concurrent console refreshes and internal reconciliation can
therefore duplicate identical upstream requests, exceed safe operational rates,
and propagate known upstream rate-limit responses as generic failures.

The protection must apply to HTTP and internal callers. A handler-only limiter
would leave background jobs, mutation enrichment, and cross-domain lookups
unprotected.

## Decision

Introduce a single process-wide information query guard at the service/provider
boundary. The application wiring will construct one guard and inject it into
every service that performs outbound WhatsApp information queries.

The guard maintains independent state per WhatsApp instance:

- A token bucket controls aggregate query rate.
- Single-flight coalesces concurrent requests by instance, operation, and
  normalized resource.
- A circuit breaker opens immediately on a typed upstream 429 response.
- An open circuit does not probe WhatsApp during cooldown.
- A half-open circuit admits one trial across the whole instance.

Single-flight admission happens before token consumption, so identical waiters
share both one token and one upstream request. Mutations are outside this guard;
only their post-action information queries use it. A failed post-action query
must not cause an already-confirmed mutation to be retried.

Initial defaults are:

```text
WA_INFO_RATE=5/min
WA_INFO_BURST=3
WA_INFO_MAX_WAIT=5s
WA_INFO_COOLDOWN=90s
```

These are conservative, configurable operational safeguards. They are not
documented as official WhatsApp limits.

The initial implementation is process-local and assumes one active process
owns a WhatsApp instance. Horizontal scaling requires an explicit instance
ownership or lease design; a distributed bucket alone does not make multiple
active whatsmeow clients safe.

## Alternatives considered

### Limit only HTTP handlers

Rejected because internal callers and reconciliation would bypass the limit,
and duplicated protection would drift across handlers.

### Use only a token bucket

Rejected because it neither coalesces identical concurrent calls nor stops
queries after WhatsApp explicitly rate-limits an instance.

### Persist or distribute guard state immediately

Deferred. The emergency containment path must not depend on the projection
foundation. Persistence can be added after versioned migrations and instance
ownership are available.

### Retry upstream 429 responses

Rejected because immediate retries increase pressure. The circuit opens and
returns a typed rate-limit error instead.

## Consequences

- All information query call sites must use the shared guard.
- Service methods that perform network work must propagate request or job
  contexts.
- The public API can consistently map typed rate-limit errors to HTTP 429.
- Per-instance state must be removed on instance deletion or final logout.
- A process restart clears limiter and breaker state until persistent state is
  introduced.
- Information lookups performed during outbound sending may affect send
  throughput and therefore require bounded caching; this does not replace the
  separate outbound message safety controls.

## Rollout and rollback

The guard core lands without changing call sites. A follow-up PR will construct
one guard in application wiring, integrate all audited information queries, and
add the public HTTP contract.

Integration will be rolled out with conservative defaults and query-level
metrics. Rollback disables the integration wiring or reverts the integration
PR; the core package and additive configuration fields do not alter behavior on
their own.
