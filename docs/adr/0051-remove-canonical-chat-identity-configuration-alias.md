# ADR 0051: Remove the canonical Chat identity configuration alias

- Status: Accepted
- Date: 2026-07-31

## Context

ADR 0050 introduced `WA_CANONICAL_CONVERSATION_IDENTITY_ENABLED` and retained
`WA_CANONICAL_CHAT_IDENTITY_ENABLED` as a temporary operator compatibility
alias. The public Chat read contract and `canonical_chat_identity` capability
had already been removed by ADR 0039. The alias was therefore the last active
Chat-named control for canonical Conversation behavior.

The compatible image at revision
`f1f4f1ea47e405114983b383a28b5fcf1e4e5256` was deployed through the controlled
development, staging, and production environments. Development and staging
first ran with both settings at the same value and emitted the expected
deprecation warning. Their active configuration was then changed to the
Conversation setting only. Production never configured either name and was
upgraded without changing that disabled behavior.

After cutover, every controlled environment was healthy on the compatible
revision with zero restarts, no startup conflicts, no deprecation warnings, and
no active configuration reference to the old name. The staging canary
reconnected successfully, completed canonical reconciliation for eleven
provider Chats without conflicts, and continued to expose ten unique canonical
Conversations.

The repository is public. There is no telemetry that can enumerate environment
variables used by external deployments, so absence of unowned consumers cannot
be proven. The project owner explicitly approved removing the alias after being
presented with that residual compatibility risk.

## Decision

Remove `WA_CANONICAL_CHAT_IDENTITY_ENABLED` from the configuration constants,
loader, development Compose manifest, tests, and current operational runbook.
Remove the temporary dual-name resolver and its warning/conflict behavior.

`WA_CANONICAL_CONVERSATION_IDENTITY_ENABLED` becomes the only supported control
for canonical Conversation identity. Its default remains `false`, and enabling
it continues to require `WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED=true`.
Conversation app-state and history-sync command flags remain independent and
continue to require canonical Conversation identity.

An environment that supplies only the retired name now receives default-off
canonical Conversation behavior. The process does not reject an unknown
environment variable because container orchestrators commonly provide settings
for multiple application versions.

No HTTP route, schema, capability, cursor, readiness predicate, database schema,
migration, or stored data changes.

## Alternatives

### Retain the alias for another release

This minimizes risk for unobserved external operators, but perpetuates the last
misnamed operational contract after all controlled environments have migrated.
Rejected by explicit owner approval.

### Fail startup when the retired name is present

This would make operator mistakes visible, but the application cannot reliably
distinguish its own retired setting from unrelated environment supplied by a
shared platform. It would also add permanent parsing solely for a removed
contract. Rejected.

### Make canonical identity always on

Canonical Conversation identity is the only public product identity and is a
candidate for baseline behavior. Removing its emergency rollout control is a
separate operational decision and is not combined with alias cleanup.

## Consequences

Current code and deployment examples contain one Conversation-named identity
setting. The temporary resolver, deprecation warning, conflict state, and tests
are removed. External operators that skipped the ADR 0050 migration must rename
their setting before upgrading or canonical Conversation readiness will remain
disabled.

Historical ADRs retain the retired name because they describe contracts that
existed when those decisions were accepted.

## Rollout and rollback

Build and publish an immutable image only after all repository gates pass. Roll
it out stop-first through development, staging canary, and production. Verify
revision identity, liveness, restart count, canonical capabilities, canonical
Conversation counts, reconciliation logs, and absence of startup errors.

The immediate rollback target is the compatible revision
`f1f4f1ea47e405114983b383a28b5fcf1e4e5256`, which understands the canonical
Conversation setting already active in development and staging. Production has
the feature disabled under both revisions. No environment translation or data
rollback is required for that rollback.

Rolling back further to a pre-ADR-0050 image requires restoring
`WA_CANONICAL_CHAT_IDENTITY_ENABLED` before startup. Restricted environment
backups and the prior immutable digests are retained for that contingency. No
database rollback is required.
