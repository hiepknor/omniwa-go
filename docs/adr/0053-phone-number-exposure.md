# ADR 0053: Instance-scoped phone identity evidence and exposure

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

Public exposure is controlled independently by
`WA_PHONE_NUMBER_EXPOSURE_ENABLED`, which also defaults to `false`. Startup
fails when exposure is enabled without evidence collection. When both flags are
enabled, the API may add digits-only `phoneNumber`, `senderPhoneNumber`,
`recipientPhoneNumber`, and `participantPhoneNumber` fields. These values do
not include a leading plus sign and are not claimed to be validated E.164
numbers. Existing JID fields and their casing remain unchanged.

The resolver reads only `projection_phone_identity_evidence`, always with the
owning instance ID. Contact lists load evidence once per request; paginated
conversation and message views use bounded batch resolution. A missing or
failed resolution omits the optional field without failing the HTTP request.
Phone-bearing HTTP responses set `Cache-Control: private, no-store`.

External Message, SendMessage, and Receipt payloads may be enriched only from
explicit PN or paired alternate metadata on the current provider event. For an
outbound SendMessage whose acknowledgement contains only a LID, the original PN
target may be carried as internal emission context only when the same provider
operation resolved that PN to the acknowledged LID before sending. This context
is not added to the raw durable event or serialized JID fields. No database
lookup is performed in the instance event handler. A shared payload policy
applies the kill switch again at delivery time for durable
webhook/RabbitMQ deliveries and at each NATS/WebSocket boundary. This redacts
queued phone fields after a restart with exposure disabled. Malformed payloads
fail closed and are not delivered.

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

Phone numbers are additive personal data. Consumers must tolerate omission and
must not treat the value as a stable account identifier. `/user/check` accepts
at most 100 numbers so evidence writes and provider queries remain bounded.

## Staged rollout

1. Deploy with both flags disabled and verify migrations, outbox health, and
   baseline response compatibility.
2. Enable evidence collection for a canary instance population while exposure
   remains disabled. Monitor evidence conflict/failure metrics.
3. Enable exposure on one canary deployment, verify tenant isolation and field
   roles across HTTP, webhook, RabbitMQ, NATS, and WebSocket consumers, then
   expand gradually.
4. Stop rollout on any cross-instance mismatch, unexpected cache retention,
   payload-policy failure, or material latency increase.

## Rollback and replacement

Set `WA_PHONE_NUMBER_EXPOSURE_ENABLED=false` and restart first. The delivery-time
policy redacts phone fields from still-queued external events; already delivered
events cannot be recalled. Evidence collection may remain enabled for diagnosis
or can be disabled in a second restart. The additive table remains unused and
safe for older application versions. A future replacement must preserve
instance provenance or require a new privacy review and ADR.
