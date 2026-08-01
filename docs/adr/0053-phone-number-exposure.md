# ADR 0053: Instance-scoped phone identity evidence

## Status

Accepted

## Context

WhatsApp may address a user by a phone-number JID or a linked-identity JID.
The whatsmeow session store contains a deployment-wide LID mapping, so using it
to populate public API responses could disclose an identity learned by a
different OmniWA instance. Phone numbers are personal data, and future public
exposure needs a tenant-scoped, auditable source of truth.

Event ingestion also runs while an instance state lock is held. Adding database
lookups to that path would increase event latency and could block unrelated
events for the same instance.

## Decision

Store only phone identities directly observed by the owning instance in
`projection_phone_identity_evidence`. A direct phone JID is evidence by itself;
a LID-to-phone relation is accepted only when both identities are present in the
same provider event or snapshot record. Global whatsmeow mappings are never an
input to this table.

Collection is controlled by `WA_PHONE_IDENTITY_EVIDENCE_ENABLED`, which defaults
to `false`. Projection workers persist evidence outside the synchronous event
handler. Conflicting relations are retained as the existing value, counted with
bounded telemetry, and never logged with raw identifiers.

Evidence remains until its instance is deleted. The instance foreign key uses
`ON DELETE CASCADE`. No global-map backfill or destructive rollback migration is
provided.

## Alternatives considered

- Use `whatsmeow_lid_map` directly. Rejected because it has no instance
  provenance.
- Resolve from `projected_contacts`. Rejected because current reconciliation can
  enrich that projection from the global map and loses provenance.
- Query the evidence table in the event handler. Rejected because the handler
  holds the instance state mutex.

## Consequences

Resolution is intentionally partial and may improve after new provider events
arrive. Historical records can therefore gain an optional phone-number view
without changing their persisted provider identities. Operators must explicitly
enable evidence collection before a later phone-exposure feature can serve it.

## Rollback and replacement

Disable collection and restart the service. The additive table remains unused
and safe for older application versions. A future replacement must preserve
instance provenance or require a new privacy review and ADR.
