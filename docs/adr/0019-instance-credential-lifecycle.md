# ADR 0019: Instance credential storage and lifecycle

## Status

Accepted

## Context

Instance tokens are bearer credentials. The current persistence model stores
them as plaintext, serializes them in ordinary instance responses, and uses a
plaintext database lookup. Removing the public field immediately would break
existing console and API consumers, while leaving the current contract in place
creates unnecessary disclosure paths.

## Decision

Adopt an expand-migrate-contract transition:

1. Add a nullable, uniquely indexed token lookup digest with a key version.
2. Dual-write plaintext and a keyed HMAC digest for new or rotated tokens.
3. Backfill existing rows in bounded, restartable batches.
4. Authenticate by digest first and measure plaintext fallback use.
5. Add an audited token-rotation endpoint and capability. A new token is shown
   only in the create or rotate response.
6. Migrate the console and API consumers to masked instance views.
7. Stop returning plaintext tokens from list and info responses.
8. Remove plaintext storage only in a later release after the rollback window.

Tokens have high generated entropy, so a keyed deterministic digest provides
indexed lookup without password-style row scanning. Digest keys live in a
secret manager and carry a version so keys can rotate. The digest, plaintext,
and rotation material never appear in logs or audit metadata.

Public instance DTOs are separate from persistence records. Compatibility is
negotiated through capabilities; storage records are never serialized directly.
Global admin-key lifecycle remains a separate product and security decision.

## Consequences

- A database read after contract completion does not reveal usable instance
  tokens.
- The migration requires a temporary dual-storage period.
- Console rollout is a dependency of contract cleanup.
- Lost tokens must be rotated rather than retrieved.

## Rollout and rollback

Every schema change is additive until plaintext removal. Authentication can
fall back to plaintext during the measured compatibility window. Token removal
is a later explicitly gated migration with backup and recovery evidence.

The public-DTO prerequisite is implemented first. Create, list, and info now
serialize an explicit compatibility view, never the persistence model. The
token field remains temporarily populated on all three paths to avoid changing
the contract before digest lookup, rotation, and console negotiation exist.
Proxy credentials and QR ceremony material are not compatibility fields and are
redacted immediately while retaining their existing JSON keys.

The digest expansion is implemented next. Migration 18 adds a nullable,
versioned HMAC-SHA-256 lookup digest with database constraints and partial
indexes. When `INSTANCE_TOKEN_HMAC_KEY` is configured, creates and updates
dual-write the current digest, authentication tries the digest before the
legacy plaintext lookup, and a bounded `FOR UPDATE SKIP LOCKED` startup worker
backfills legacy rows. The worker is finite per process start and restartable;
it records only aggregate counts. Plaintext remains the rollback path during
this phase. Operators must use the same base64-encoded key and key version on
every replica and retain the old secret until a later measured rotation phase.

The audited rotation phase is implemented by migration 19 and the admin-only
`POST /instance/rotate-token/{instanceId}` endpoint. Callers submit the current
`expectedVersion` and a bounded reason. The repository uses a compare-and-swap
update, so concurrent operators cannot silently invalidate a token another
operator has just received. The new high-entropy token is returned once; audit
rows contain only the generation transition, reason, request ID, and a
domain-separated hash of the admin actor credential. The database update and
audit insert share one transaction. A successful rotation also updates the
active runtime's token under synchronization so subsequent event envelopes use
the current credential.

The admin capability `instance_token_rotation` is advertised only when the HMAC
key is configured. Instance-scoped capability calls do not receive this
administrative capability. Public instance views expose the additive
`credentialVersion` needed for optimistic concurrency; they continue returning
the legacy token until the Console migration and rollback measurement required
by the contract phase are complete.
