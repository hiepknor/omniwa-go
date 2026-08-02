# ADR 0059: Run database migrations as a one-shot command

- Status: Accepted
- Date: 2026-08-03

## Context

ADR 0056 selected an active-passive modular monolith and ADR 0058 established
one owner for the active runtime lifecycle. Database schema changes are still
embedded in server startup. That prevents a future standby process from being
started without performing DDL and gives operators no independently verifiable
migration step before application promotion.

The process owns three schema groups: legacy GORM users tables plus versioned
users migrations, the whatsmeow auth store, and the licensed runtime table when
the license gate is enabled. Previously, auth-store upgrades occurred lazily
when a WhatsApp client first started, so successful HTTP startup did not prove
that every required schema could be upgraded.

This is an L3 database and deployment-lifecycle change. The existing poll table
is adopted as auth-database migration 1 so its prior constructor-time DDL is no
longer outside the runner. Users and auth migrations have separate version
tables and checksums because they are separate databases. There is no
destructive operation or public HTTP contract in this decision.

## Decision

The existing binary accepts an additive `migrate` subcommand. With no command it
continues to start the server exactly as before. The command:

1. loads only users/auth database configuration and the license-gate switch;
2. opens the users database and takes the existing single-replica ownership
   lock;
3. upgrades the base users tables and all checksum-protected versioned
   migrations, including adoption of the existing poll schema;
4. upgrades the whatsmeow auth store;
5. upgrades the licensed runtime table only when the gate is enabled;
6. closes all connections, releases ownership, and exits.

The normal server startup calls the same migration runner while holding the
same ownership lock. Automatic startup migration remains enabled in this slice
for backward compatibility. A later cold-standby change may require external
migrations, but only after this command has been deployed and exercised.

The production Compose file includes an operations-profile migration service.
It receives only database DSNs and does not receive the global API key or event,
storage, and provider credentials. Operators stop the active application before
running the job. An attempted migration while the application owns the users
database fails closed.

## Alternatives

### Keep migrations only in server startup

This requires every candidate process to perform DDL and cannot distinguish
migration failure from the rest of application bootstrap. It was rejected as a
blocker for a passive process lifecycle.

### Let the migration command run beside an active server

The versioned registry has its own transaction lock, but GORM base migration,
auth-store upgrades, and licensed schema migration do not share that lock. It
was rejected because live DDL would have inconsistent coordination boundaries.

### Require the full application configuration for migration

This would expose API, transport, and storage credentials to a short-lived
database job. A minimal configuration loader and least-privilege Compose
service were selected instead.

### Disable automatic startup migration immediately

Existing native, Compose, and downstream deployments rely on the current
behavior. An immediate switch could start an old or incomplete schema or break
uncoordinated upgrades, so that change is deferred to the cold-standby rollout.

## Consequences

- Operators can verify migration success independently before starting the
  application.
- Auth schema failures surface even when no WhatsApp instance connects.
- Repeated commands are safe through existing idempotency and checksums.
- A running active application intentionally blocks the migration job.
- Server startup still performs idempotent migration work until the later
  external-migration mode is introduced.

## Acceptance, rollout, and rollback

Acceptance requires CLI parsing tests, minimal/file-backed configuration tests,
existing empty/repeated/concurrent PostgreSQL migration tests, full race tests,
and a container smoke test that runs the command twice before startup, verifies
both users and auth schemas, rejects the command while the app is active, and
survives restart.

Deploy the image without changing the current application command. In staging,
stop the application, run the migration service twice, start the application,
and verify readiness and schema state. Only then adopt the job in production.
Rollback restores the previous immutable image. Since this slice adds no schema
and all existing migrations are forward-only, no data rewrite or down migration
is required.
