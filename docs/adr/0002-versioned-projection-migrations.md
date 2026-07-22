# ADR 0002: Versioned migrations for projection schema

- Status: Accepted
- Date: 2026-07-22

## Context

The upstream-derived tables are currently created with GORM `AutoMigrate`.
Projection schema needs deterministic ordering, immutable history, concurrency
control during deploys, and evidence of exactly which changes reached a
database. `AutoMigrate` alone does not provide those properties.

## Decision

OmniWA GO owns an ordered migration registry in `pkg/migrations` for all new
projection schema. Each migration has a monotonically increasing version, a
stable name, SQL, and a SHA-256 checksum. Startup applies pending migrations in
one PostgreSQL transaction while holding a transaction-scoped advisory lock.
Applied versions are stored in `schema_migrations`; changing the name or SQL of
an applied migration makes startup fail safely.

The first migration creates durable per-instance/per-resource projection state
and constrains its lifecycle status in PostgreSQL. Instance deletion cascades
to projection state.

Legacy upstream tables continue through the existing `AutoMigrate` call for
now. New projection tables and changes must not be added to that call. They
must use forward-only, additive versioned migrations.

## Rollout and rollback

Migrations run before services and background workers start. Multiple replicas
may start concurrently; the advisory lock serializes their migration work.

Migrations use expand/contract changes and do not include destructive down
migrations. The previous application version must remain compatible during the
rollout window. Application rollback therefore means deploying the previous
binary and leaving additive schema in place. Destructive cleanup requires a
later, separately approved migration after the compatibility window.

## Consequences

- Projection state survives process restarts.
- Drift in an already-applied migration is detected at startup.
- A failed migration rolls back its schema and history record together.
- PostgreSQL is an explicit requirement for the users/projection database.
- Migration PRs require SQL review, integration coverage, rollout notes, and a
  tested application rollback path.
