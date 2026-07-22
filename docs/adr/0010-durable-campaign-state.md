# ADR 0010: Durable campaign and recipient state

## Status

Accepted

## Context

Outbound pacing alone cannot provide safe campaign orchestration. A restart or
concurrent worker must not lose lifecycle state, resend completed recipients,
or operate without recorded consent.

## Decision

Persist campaigns, recipients, and append-only audit events in PostgreSQL with
versioned migration 12. Campaigns begin as drafts and follow an explicit state
machine. Transitions lock the current row, compare its version and status, and
append the audit event in the same transaction.

Every recipient must be a normalized direct-user WhatsApp JID and include an
opt-in source, timestamp, and evidence reference. Only a campaign-scoped
SHA-256 digest of the evidence reference is stored, preventing cross-campaign
correlation; raw evidence, API keys, instance tokens, and provider payloads are
not campaign data. Duplicate canonical recipients are rejected per campaign.

Recipient rows include durable scheduling, lease, attempt, provider message ID,
delivery lifecycle, and safe error-code fields. The processing invariant is
enforced by a database check: a processing row must have both a claim token and
lease, and other states must not retain them. Worker claim and completion
operations will be added behind this schema using bounded `SKIP LOCKED` claims.

The first content contract is normalized text with a 4,096-character bound.
Provider-native message payloads are not accepted. New content types require an
additive migration, validation contract, and independent safety review.

## Consequences

- Consent evidence is structurally required before a recipient can exist.
- Campaign state survives restarts and supports multi-replica coordination.
- Audit history cannot diverge from a successful lifecycle transition.
- Public APIs and workers can be introduced separately without relying on
  AutoMigrate or inventing persistence later.
