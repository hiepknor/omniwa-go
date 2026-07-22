# Security and reliability remediation plan

This document is the implementation index for the accepted security and
reliability decisions. It complements `ENGINEERING_WORKFLOW.md`; it does not
replace that workflow or lower its risk gates.

## Baseline

The baseline was captured from `main` at commit `7f9e6ac` on 2026-07-22.

| Signal | Baseline |
|---|---:|
| Plaintext instance-token log sites | 5 |
| Direct production `http.Get` call sites outside `pkg/core` | 11 |
| `http.MaxBytesReader` call sites | 2 |
| PostgreSQL test files gated by `TEST_POSTGRES_DSN` | 6 |
| HTTP 500 paths returning `err.Error()` directly | 70 |
| Plain shared runtime maps created in the composition root | 2 |
| Reachable vulnerabilities reported by `govulncheck ./...` | 4 |

The development database also demonstrated a permanently failing group event
retrying more than 80 times while the group projection state remained `ready`.
The container registry demonstrated a mutable `latest` image at revision
`662290d` while repository `main` was at `7f9e6ac`.

These values are characterization evidence, not permanent quality targets. A
raw-call count may change as code evolves; the completion conditions below are
the authoritative requirements.

## Accepted decisions

- [ADR 0015](./adr/0015-model-and-module-boundaries.md): preserve the modular
  monolith while separating persistence, domain, provider, and public models.
- [ADR 0016](./adr/0016-instance-runtime-ownership.md): one fenced runtime owner
  per instance and a session-based WebSocket hub.
- [ADR 0017](./adr/0017-safe-outbound-network-access.md): policy-based bounded
  outbound network access.
- [ADR 0018](./adr/0018-projection-failure-and-readiness.md): bounded retries,
  dead letters, and lag-aware readiness.
- [ADR 0019](./adr/0019-instance-credential-lifecycle.md): one-time credentials,
  keyed lookup digests, and staged rotation.
- [ADR 0020](./adr/0020-immutable-artifact-promotion.md): build once and promote
  immutable digests.

## Delivery sequence

Each numbered item is a focused branch and pull request. Intermediate states
must remain deployable.

### 1. Emergency containment

1. Remove and prevent plaintext credential logging; rotate exposed development
   and staging credentials through an operational runbook.
2. Add a remote-media kill switch, public-address enforcement, timeouts, and
   initial size limits.
3. Upgrade only the dependencies required to remove reachable vulnerabilities.
4. Publish and deploy development images by source SHA or digest.

### 2. Bounded platform foundations

5. Introduce the safe network client and migrate callers by policy category.
6. Enforce route-specific body limits, server timeouts, cancellable media
   processors, and a non-root runtime image.
7. Centralize public-safe error mapping and request identities.

### 3. Runtime and realtime correctness

8. Introduce the fenced instance runtime registry and migrate all raw-map
   callers.
9. Replace the single-connection WebSocket map with bounded per-session write
   pumps.
10. Enforce and document the single-application-replica topology with a
    monitored PostgreSQL advisory lock until distributed leases exist.

### 4. Projection reliability

11. Add dead-letter and failure metadata through an additive migration.
12. Add typed retry classification, exponential backoff, and retry ceilings.
13. Derive projection health from state, lag, reconciliation age, and dead
    letters.
14. Add audited inspection, replay, and discard operations using safe summaries.

### 5. Credential and public-model migration

15. Introduce explicit public instance DTOs without serializing persistence
    records.
16. Add dual-write and dual-read token lookup digests and a bounded backfill.
17. Add audited token rotation and console capability negotiation.
18. Stop exposing and later remove plaintext tokens only after console migration
    and a measured rollback window.

### 6. Maintainability, CI, and release integrity

19. Extract worker and runtime wiring into `pkg/bootstrap` without moving the
    entrypoint or editing `pkg/core`.
20. Add architecture checks for reverse imports, raw network calls, sensitive
    serialization, and raw runtime maps.
21. Make PostgreSQL integration, race, vulnerability, Swagger-drift, and secret
    checks mandatory in CI. Completed: the existing required
    `build / vet / test` check now enforces all five gates with pinned Go tools
    and an isolated PostgreSQL service.
22. Add a container migration/liveness/revision smoke test. Completed: CI
    builds the production Dockerfile and verifies artifact identity, non-root
    execution, liveness, migration completeness, restart survival, and
    migration idempotency against isolated PostgreSQL.
23. Replace mutable build-and-push behavior with digest promotion. Completed:
    successful main CI publishes one immutable SHA image with SBOM and
    provenance, while Git releases promote that existing digest without a
    rebuild or movable release alias.
24. Bound webhook delivery workers without claiming a public durable-history
    contract. Completed: the implementation uses bounded global and
    per-instance admission, a fixed worker pool, cancellable classified retries,
    safe counters/logs, and supervisor-owned shutdown.

### 7. Optional high availability

Distributed instance-owner leases, owner-aware campaign claims, and
cross-replica realtime fan-out are not started until multi-replica operation is
an accepted requirement. Until then, one application replica is a hard
invariant.

## Cross-repository compatibility

Backend changes land additively first. OmniWA Console then supports both the old
and new contract, using capabilities, stable error codes, and projection
metadata. Legacy behavior is removed only after console deployment and measured
fallback usage show it is safe.

Opaque cursors, error text, persistence fields, and provider-native payloads
are never compatibility interfaces.

## Required verification

All pull requests run `go build ./...`, `go vet ./...`, the complete test suite
with and without a real PostgreSQL service, `go test -race ./...`,
`govulncheck`, deterministic Swagger regeneration with a clean-tree assertion,
and a committed-secret scan. Local verification also runs `git diff --check`.
Risk-specific gates include:

- `go test -race ./...` for runtime, WebSocket, guard, and worker changes.
- `go test ./pkg/architecture` for dependency direction, guarded network
  access, sensitive model serialization, and runtime-registry ownership.
- Empty, existing, repeated, populated, and concurrent PostgreSQL migration
  tests for schema changes.
- DNS rebinding, redirect, private address, timeout, cancellation, MIME, and
  size tests for outbound network changes.
- Multi-tab, slow-consumer, stale-session, and concurrent-producer tests for
  WebSocket changes.
- Duplicate, out-of-order, poison, replay, and restart tests for projections.
- `govulncheck ./...` and media/database regression tests for dependencies.
- Container startup evidence that source SHA, OCI revision, runtime revision,
  and applied migration version match expectations.

## Program completion conditions

The program is complete only when:

- logs and ordinary responses contain no bearer credentials;
- caller-provided media cannot reach private networks or consume unbounded
  resources;
- reachable vulnerability scans are clear;
- runtime and WebSocket concurrency tests pass under the race detector;
- poison events dead-letter and projection health becomes stale rather than
  remaining falsely ready;
- PostgreSQL integration tests are mandatory in CI;
- out-of-order builds cannot roll a deployed revision backward;
- source, artifact, migration, runtime, and console capabilities are verifiably
  aligned; and
- completed branches are squash-merged and deleted locally and remotely.
