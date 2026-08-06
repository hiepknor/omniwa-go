# Independent CI Runbook

This runbook adds an independent verification path for GitHub Actions outages.
It does not authorize bypassing repository verification.

## Security boundary

Provision the `omniwa-ci-ephemeral` Buildkite queue on disposable, single-job
agents. Each agent must have Go 1.25.x, Docker, Git, Bash, Python 3, curl, and jq.
Agents must not have application, cloud, deployment, registry-write, GitHub
write, or WhatsApp credentials. Deny network access to development, staging,
production, private databases, and provider endpoints.

Configure the Buildkite GitHub integration for only
`github.com/hiepknor/omniwa-go`. Do not run fork pull requests on the trusted
queue. The repository wrapper independently enforces the canonical repository,
fork, and exact-commit checks before starting test services.

## Enable shadow verification

1. Create a Buildkite pipeline using the committed `.buildkite/pipeline.yml`.
2. Attach only the `omniwa-ci-ephemeral` queue.
3. Enable builds for pull requests from the canonical repository and pushes to
   `main`; leave fork builds disabled.
4. Keep the Buildkite status check non-required.
5. Compare GitHub Actions and Buildkite outcomes for at least five commits,
   including one pull request and one `main` push. Investigate every mismatch.

The entrypoint is intentionally fail-closed. Missing service endpoints,
repository mismatches, and commit mismatches fail the job. Test-container
cleanup is best-effort so it cannot hide the original verification result; the
agent must be destroyed after the job to provide the final cleanup boundary.

## Change the required check

Treat branch protection as an external security-sensitive change. Record the
current rule before editing it and obtain repository-owner approval.

After the shadow period passes:

1. Make the Buildkite verification status required for `main`.
2. Confirm a new pull request cannot merge until that exact commit passes.
3. Remove `build / vet / test` from the required set. Keep GitHub Actions
   enabled as a non-required secondary signal.
4. Confirm a documentation-only pull request receives both checks and that the
   independent check gates merge.

Do not require both providers long-term: that increases the number of outages
that block delivery. Never temporarily disable all required verification.

## Outage operation

During a GitHub Actions outage, verify the Buildkite job ran for the pull
request's current 40-character commit SHA. A stale successful check does not
authorize merge. If GitHub cannot accept status updates, leave the merge
blocked; do not use an administrative bypass.

## Rollback

If Buildkite becomes unreliable, restore `build / vet / test` as required and
then remove the Buildkite check from the required set. Preserve evidence of the
change and verify the next pull request is gated before considering rollback
complete.
