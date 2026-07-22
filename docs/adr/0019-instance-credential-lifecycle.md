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
