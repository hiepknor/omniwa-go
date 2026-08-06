# ADR 0070: Run Independent CI Verification

## Status

Accepted

## Context

The protected `main` branch depends on the `build / vet / test` status check.
When GitHub-hosted Actions cannot start jobs, repository verification cannot
run and merges correctly remain blocked. Retrying jobs or moving to a
self-hosted GitHub Actions runner does not remove the shared Actions control
plane dependency.

The verification contract includes compilation, static analysis, unit and
integration tests, the race detector, vulnerability and secret scanning,
generated-document drift checks, deployment validation, and a production-image
smoke test. A second CI system must not silently implement a weaker contract.

## Decision

Maintain `scripts/ci/verify-repository.sh` as the single verification
entrypoint. GitHub Actions and an independent Buildkite pipeline both invoke
that entrypoint.

Buildkite jobs run only on an isolated, ephemeral agent queue. The wrapper:

- accepts only the canonical repository URL;
- requires the checked-out commit to exactly match `BUILDKITE_COMMIT`;
- rejects fork pull requests on the trusted queue;
- creates isolated PostgreSQL and RabbitMQ test containers with fixture-only
  credentials; and
- removes those containers when the job exits.

The agent has no application credentials and no network route to development,
staging, production, or WhatsApp provider endpoints. Required-check migration
is a separate, manually approved rollout after both providers have produced
equivalent results for several commits.

## Alternatives considered

- **Retry GitHub Actions jobs.** Useful for transient failures, but it does not
  provide an independent execution path during a provider outage.
- **Use a self-hosted GitHub Actions runner.** This controls compute capacity
  but still depends on the GitHub Actions scheduler and control plane.
- **Make CI optional during an outage.** Rejected because it turns an
  availability incident into an integrity and supply-chain risk.
- **Duplicate verification commands in Buildkite.** Rejected because the two
  gates would drift and could report success for different contracts.

## Consequences

Verification can execute outside GitHub Actions while preserving the existing
test contract. GitHub availability is still needed to receive source and post a
status check, so this is resilience against an Actions outage, not complete
independence from GitHub.

The organization must operate a patched, disposable agent image with Go
1.25.x, Docker, Git, Bash, Python 3, curl, and jq. Buildkite integration and
branch-protection changes remain external security-sensitive operations.

## Rollout and rollback

Follow the independent CI runbook. First run Buildkite in non-required shadow
mode. After equivalent results are observed, make the Buildkite check required
and remove the GitHub Actions check from the required set so either provider's
outage cannot require bypassing two gates.

If the independent path is unreliable, restore `build / vet / test` as the
required check and remove the Buildkite check from the required set. The shared
entrypoint remains usable by GitHub Actions and local CI hosts.
