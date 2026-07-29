# ADR 0034: Admin-targeted capabilities and safe history-sync diagnostics

## Status

Accepted.

## Context

Projection capabilities are readiness-scoped per instance. An instance token
provides that identity to `GET /server/capabilities`, but a global admin
credential has no implicit instance identity. Returning only server and admin
capabilities for an untargeted admin request is correct, but it does not let an
administrative Console negotiate the contract of the selected instance.

Message readiness can also fail while normalizing a provider history-sync
chunk before its completion barrier is ingested. The previous log recorded
only `history_sync_failed`. That protected provider data but discarded the
safe stage and ordinal needed to diagnose or reproduce the failure.

## Decision

This decision additively amends ADR 0031: an untargeted admin response still
omits `instanceId`, while a targeted admin response includes it.

`GET /server/capabilities` accepts an optional `instanceId` query parameter.
Only a global admin credential may use it to select another instance. The
handler validates the UUID and verifies the target through the instance
repository before evaluating projection state. A targeted response retains
`credentialScope: "admin"` and includes `instanceId`.

An instance credential remains bound to the UUID resolved during
authentication. Supplying its own UUID is harmless; supplying any other UUID
returns `instance_scope_mismatch` without evaluating that target.

History-sync ingestion returns a typed internal failure carrying only a stable
stage, machine-readable error code, zero-based conversation ordinal, and
zero-based message ordinal. Operational logs include those bounded fields but
not the underlying error, provider identity, message content, or raw payload.
Projection state remains failed or stale until a valid completion barrier is
processed; diagnostics never promote readiness.

## Consequences

- Administrative clients can negotiate per-instance projection capabilities
  without handling instance bearer tokens.
- Untargeted admin and existing instance responses remain backward compatible.
- Capability strings are deduplicated before returning a targeted mixed set.
- Unknown admin targets return `instance_not_found`; cross-instance tokens do
  not reveal whether the requested UUID exists.
- Future failures have actionable stage and ordinal evidence. Historical
  failures logged before this change cannot recover their discarded cause.
- This decision adds no database migration and performs no live WhatsApp read.

## Alternatives

### Infer instance readiness from the binary version

Rejected. Readiness is persisted independently for every instance and can be
syncing, stale, or failed on the same binary.

### Give the Console every instance bearer token

Rejected. It expands secret distribution and bypasses the established admin
credential boundary.

### Log the complete history-sync error or payload

Rejected. Provider errors and payloads can contain JIDs, message content, or
other private data. Stable stages and ordinals are sufficient to locate the
failing normalization path safely.

## Rollout and rollback

Deploy the backend before changing administrative clients. Clients continue
to use an untargeted request for server capabilities and add `instanceId` only
after a user selects an instance. No capability is inferred when the targeted
request fails.

Rollback removes the optional query behavior and enhanced diagnostic fields;
it does not change projection data or readiness. Clients must tolerate a
targeted request being unsupported during a mixed rollout and must not replace
that uncertainty with version inference.
