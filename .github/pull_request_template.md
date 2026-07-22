## Problem

<!-- What problem does this PR solve? Include current behavior or reproduction evidence. -->

## Solution

<!-- Explain the implementation and why this approach was chosen. -->

## Scope

<!-- List the components and behaviors changed by this PR. -->

## Non-goals

<!-- State what is intentionally deferred or excluded. -->

## Risk classification

- [ ] L1 — documentation, tooling, or small isolated change
- [ ] L2 — business logic, API behavior, or normal feature
- [ ] L3 — migration, auth, concurrency, public contract, or distributed state
- [ ] L4 — destructive, security-sensitive, or irreversible external action

## Compatibility

<!-- Describe API, database, configuration, event, and deployment compatibility. -->

- [ ] Existing public contracts remain compatible, or the breaking change is
      explicitly approved and versioned.
- [ ] Swagger and user-facing documentation are updated when required.
- [ ] Database changes follow an additive, versioned migration path when
      required.

## Risks and mitigations

<!-- Include failure modes, security/privacy impact, and concurrency concerns. -->

## Validation

<!-- Record exact commands and results. Do not check an item that was not run. -->

- [ ] `git diff --check`
- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `go test -race ./...` when concurrency-sensitive
- [ ] Migration/integration/Docker checks when required
- [ ] Manual verification when required

## Rollout

<!-- Feature flags, ordering, shadow/canary steps, monitoring, and exit criteria. -->

## Rollback

<!-- Exact disable/revert/recovery procedure and any data constraints. -->

## Review checklist

- [ ] The PR has one primary objective and no unrelated changes.
- [ ] Acceptance criteria are testable and demonstrated.
- [ ] Internal callers and failure paths were reviewed.
- [ ] Errors, timeouts, retries, and partial success are handled intentionally.
- [ ] Shared state and background work are bounded and race-safe.
- [ ] No secrets or sensitive payloads are logged.
- [ ] The complete diff was self-reviewed.
- [ ] CI must be green before merge.

## Follow-up

<!-- Link deferred work, cleanup, rollout tasks, or related PRs. -->
