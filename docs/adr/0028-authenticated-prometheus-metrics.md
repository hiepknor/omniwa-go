# ADR 0028: Authenticated Prometheus metrics

## Status

Accepted

## Context

OmniWA GO has persisted operational summaries and health endpoints, but it has
no process metrics registry or scrape endpoint. Group List eligibility needs
request latency, batch-size, result-count, and mutation-rejection metrics. The
labels must remain bounded and must never expose instance identities, WhatsApp
JIDs, participant data, credentials, or provider errors.

The upstream `pkg/telemetry` package sends route telemetry to a remote service.
It is not an operator-owned metrics backend and is not an acceptable place for
fork-specific business metrics.

## Decision

The application owns an isolated Prometheus registry instead of using the
global registry. It registers Go and process collectors plus explicitly
constructed domain collectors. Domain services depend on narrow observer
interfaces and do not import Prometheus.

`GET /metrics` exposes the Prometheus text format and requires the global admin
key in the existing `apikey` header. Instance tokens cannot scrape metrics.
Scrapers therefore use the same secret-management and rotation controls as
other admin operations.

Every label value is checked against a compile-time allowlist before a metric
is recorded. Invalid operations, states, rejection codes, negative values, or
inconsistent result counts are dropped. Instance IDs, group JIDs, reasons from
providers, evidence references, and API keys are not labels.

## Alternatives considered

- Extending upstream route telemetry was rejected because it sends data to an
  external service and does not provide operator-controlled scraping.
- Structured logs alone were rejected because they do not satisfy the required
  counter and histogram contract.
- An unauthenticated endpoint was rejected because operational metrics should
  not be exposed to every network peer that can reach the API.
- The global Prometheus registry was rejected because it creates hidden process
  state and duplicate-registration failures in tests and embedded runtimes.

## Consequences

- Operators must configure Prometheus to send `apikey: <GLOBAL_API_KEY>`.
- New metric families require an explicit observer contract and bounded-label
  review.
- Prometheus becomes a direct application dependency.
- Metrics are process-local and reset on restart. Durable business state
  remains owned by PostgreSQL and the existing overview endpoints.

## Rollout and rollback

Deploy the endpoint before instrumenting Group List eligibility, verify admin
authentication and scrape health, and then add the bounded eligibility
observations. Rollback reverts the endpoint and registry wiring; it does not
require a database or data migration.
