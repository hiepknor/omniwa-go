# Engineering Implementation Workflow

This document defines the mandatory, reusable workflow for implementing changes
in OmniWA GO. It applies to fixes, features, refactors, migrations, operational
changes, and multi-stage engineering goals.

The workflow is designed to keep changes reviewable, reversible, compatible,
and safe to deploy while preserving the fork's ability to merge upstream.

## 1. Risk classification

Classify the task before changing repository files. When uncertain, use the
higher risk level.

| Level | Examples | Required controls |
|---|---|---|
| L0 | Read-only investigation or analysis | Evidence-backed report; no branch or PR required |
| L1 | Documentation, tooling, or a small isolated change | Branch, repository validation, and PR |
| L2 | Business logic, API behavior, or a normal feature | Written design note, tests, compatibility review, and rollback plan |
| L3 | Database migration, authentication, concurrency, public contract, or distributed state | ADR when difficult to reverse, staged rollout, integration and failure tests, tested rollback |
| L4 | Destructive data operation, security-sensitive production action, or irreversible external change | Explicit approval, recovery or backup plan, manual execution gate, and post-action verification |

Risk classification controls the minimum process. It never overrides the hard
rules in `AGENTS.md`.

## 2. Definition of Ready

Do not begin implementation until the following questions have concrete
answers:

- What is the objective?
- What testable acceptance criteria define success?
- What is explicitly out of scope?
- What current behavior and public contracts must remain compatible?
- Which handlers, services, repositories, providers, jobs, and internal callers
  are affected?
- Does the change affect a database, external system, security boundary,
  concurrency model, or customer data?
- What are the expected failure modes?
- How will the change be rolled back or disabled?
- Does it require a feature flag, shadow mode, backfill, or staged migration?
- Which earlier tasks or PRs must land first?

For a defect, reproduce it or add a characterization test before changing the
behavior whenever practical. For a public API change, capture the current
request, success response, and error contract.

## 3. Repository preparation

Before editing:

1. Read `AGENTS.md` and the relevant domain documentation.
2. Check the current branch and working tree.
3. Preserve unrelated or uncommitted user changes.
4. Fetch `origin/main` and verify that local `main` is up to date.
5. Create a dedicated branch from `main` using
   `<type>/<short-description>`.

Never implement directly on `main`. Never combine unrelated work in the task
branch.

## 4. Discovery and blast-radius analysis

Trace the complete behavior before editing:

- Follow the entrypoint through handler, service, repository, provider, and
  event paths.
- Find internal callers in addition to HTTP callers.
- Identify shared state, interfaces, network calls, database writes, goroutines,
  retries, and caches.
- Check where errors are wrapped, swallowed, translated, or converted to HTTP
  responses.
- Identify backward-compatibility constraints and generated documentation.
- Inspect existing tests and record important coverage gaps.

For L2 or higher, record a short implementation note containing:

- Current behavior.
- Affected components.
- Compatibility constraints.
- Risks and failure modes.
- Test strategy.
- Rollout and rollback strategy.

## 5. Design gate

The design must be reviewable before substantial implementation begins. Cover
the areas relevant to the task:

- State ownership and lifecycle.
- Interface and dependency boundaries.
- Typed error model and public error mapping.
- Context propagation, cancellation, and timeouts.
- Concurrency limits and bounded resource use.
- Idempotency and retry behavior.
- Transaction boundaries and persistence semantics.
- Security, privacy, and secret handling.
- Logs, metrics, health, and alert signals.
- Backward compatibility.
- Rollout, rollback, and data recovery.

Write an ADR under `docs/adr/` for an L3 or L4 decision that is expensive to
reverse, affects multiple domains, or establishes a long-lived architectural
contract. An ADR must include context, decision, alternatives, consequences,
and rollback or replacement conditions.

## 6. Slice work into deployable increments

Prefer small PRs that each have one primary objective. Every intermediate state
must build, test, and remain safe to deploy.

A typical high-risk rollout is:

```text
foundation
  -> integration behind a safe flag
  -> shadow or canary mode
  -> serve mode
  -> cleanup in a later release
```

Do not combine additive schema, behavior switching, backfill, and destructive
cleanup in one PR. Do not merge code that requires an unmerged dependency to be
safe.

## 7. Implementation rules

During implementation:

- Pass `context.Context` through network and database boundaries.
- Bound goroutines, queues, caches, retries, and wait times.
- Do not retry a mutation unless it is idempotent or protected by an
  idempotency key.
- Use typed errors with `errors.Is` or `errors.As`; do not parse strings when a
  typed error is available.
- Keep cross-cutting behavior such as authentication, rate limiting, error
  mapping, and logging at a shared boundary rather than copying it into
  handlers.
- Use parameterized database queries.
- Protect shared state from data races and define its cleanup lifecycle.
- Never log tokens, API keys, passwords, or sensitive raw payloads.
- Make public API changes additive unless a versioned breaking change has been
  explicitly approved.
- Give feature flags a safe default and a documented rollback setting.
- Keep commits coherent and buildable.
- Follow all upstream-sync and licensing constraints in `AGENTS.md`.

## 8. Database changes

Use versioned migrations for new persistent structures. Apply the
expand-migrate-contract pattern:

```text
additive schema
  -> application compatible with old and new schema
  -> bounded and restartable backfill
  -> switch reads and writes
  -> observe
  -> destructive cleanup in a later release
```

Migration requirements:

- Safe execution on both an empty database and an existing supported schema.
- Idempotent migration runner behavior.
- Protection from concurrent migration execution.
- Explicit indexes and uniqueness constraints.
- Forward-fix strategy for migrations that cannot be safely reversed.
- No destructive change without an approved backup or recovery plan.
- No long-running full-table work hidden inside an HTTP request.

## 9. Testing strategy

Tests are selected by behavior and risk, not merely by which file changed.

Use the relevant layers:

- Unit tests for state machines, validation, and business rules.
- Concurrency tests for single-flight, limiters, locks, cancellation, and
  lifecycle cleanup.
- Integration tests across handler, service, repository, and provider
  boundaries.
- Contract tests for existing public request and response shapes.
- Negative tests for timeouts, cancellation, malformed input, upstream errors,
  and partial failures.
- Migration tests for empty, populated, repeated, and concurrent startup cases.
- Restart tests for durable state.
- Idempotency tests for duplicate and out-of-order events.

Prefer fake clocks and injectable timers over long sleeps. Use narrow provider
interfaces or fakes instead of making tests depend on live external systems.

Before opening any PR, run:

```bash
git diff --check
go build ./...
go vet ./...
go test ./...
```

For concurrency-sensitive changes, also run:

```bash
go test -race ./...
```

Run `make swagger` after changing handlers, annotations, or public API
contracts. Run additional migration, integration, or Docker checks required by
the task's risk level.

The required `build / vet / test` CI check also runs the full test suite against
a real PostgreSQL service, the race detector, `govulncheck`, deterministic
Swagger regeneration, and a committed-secret scan. These gates are mandatory
for every pull request; risk-specific local checks supplement them rather than
replace them. The .gitleaksignore file records only the fingerprints of
pre-existing fixture and documentation findings. Review any ignore-list change
as security-sensitive; never update it merely to make CI pass.

## 10. Mandatory self-review

Read the complete diff before committing and verify:

- The diff contains no unrelated changes.
- Every known caller and internal path is covered.
- Errors are preserved and mapped intentionally.
- No known failure is accidentally converted to HTTP 500.
- There is no unsafe mutation retry or partial-success ambiguity.
- Shared state is race-safe and bounded.
- Context cancellation cannot leak work or goroutines.
- Public contracts remain compatible or are explicitly versioned.
- Database changes are safe for existing data.
- Logs and metrics contain no secrets.
- New dependencies are justified.
- Failure modes are observable.
- Rollback is practical and documented.
- Swagger and documentation match the implementation.

## 11. Commit and pull request

Use Conventional Commits. Push the task branch and open a PR targeting `main`.
The PR must document:

- Problem and objective.
- Solution and scope.
- Non-goals.
- Compatibility impact.
- Risks and mitigations.
- Exact validation commands and results.
- Rollout plan.
- Rollback plan.
- Follow-up work.

Attach contract examples, migration evidence, screenshots, or metrics when they
materially help review. Never claim a check passed unless it was actually run.

## 12. Review and merge gate

Merge only when:

- Acceptance criteria are demonstrated.
- Required local checks and CI pass.
- Review comments are resolved.
- Migration, security, concurrency, and compatibility concerns are resolved.
- The branch is current and mergeable with `main`.
- Rollout and rollback instructions are complete.

Use squash merge. Do not bypass failing CI for production changes.

## 13. Staged rollout

For L3 and L4 changes:

1. Deploy foundations without switching behavior.
2. Enable the feature in its safest mode.
3. Use shadow, canary, or a limited instance cohort when possible.
4. Monitor error rate, latency, saturation, queue depth, state lag, and data
   divergence.
5. Advance only after measurable exit criteria are met.
6. Roll back when a predefined threshold is breached.
7. Perform destructive cleanup only in a later release after the rollback
   window has closed.

## 14. Post-merge closure

After merge:

1. Switch to `main` and fast-forward from `origin/main`.
2. Verify the expected merge commit.
3. Delete the task branch remotely and locally.
4. Prune stale remote references.
5. Confirm that the working tree is clean.
6. Monitor CI and deployment when the change has runtime impact.
7. Create follow-up issues or PRs for explicitly deferred work.

## Definition of Done

A repository-changing task is complete only when:

- Its acceptance criteria are met.
- Required tests and documentation are complete.
- The complete diff has been reviewed.
- The branch is pushed and the PR URL is reported.
- CI is green before merge.
- Rollout and rollback status are reported when applicable.
- The local and remote task branches are deleted after merge.
- No unexplained local changes remain.

Read-only investigations end with an evidence-backed report and do not require
a branch or PR.
