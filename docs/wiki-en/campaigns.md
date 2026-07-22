# Campaign orchestration

Campaigns are durable, instance-scoped text delivery jobs. They use persisted
recipient state, explicit opt-in evidence, lifecycle controls, audit history,
and the shared per-instance outbound message guard.

## Safety contract

- Use an instance token in the `apikey` header. The global admin key is not
  accepted by campaign routes.
- Every recipient must be a direct WhatsApp JID and include `optInSource`,
  `optInEvidenceReference`, and `optedInAt`.
- OmniWA GO hashes evidence references before persistence. It does not verify
  that the caller's consent assertion is legally or operationally sufficient.
- Drafts are limited to 10,000 recipients and request bodies to 8 MiB.
- Pause or abort stops new claims. A recipient already leased by a worker may
  still finish.
- Delivery is at-least-once across the external provider boundary. Stable
  message IDs reduce duplicate risk but do not establish exactly-once delivery.

## Create and activate

Create a draft:

```http
POST /campaigns
apikey: <instance-token>
Content-Type: application/json
```

```json
{
  "name": "Order update",
  "text": "Your order is ready.",
  "recipients": [
    {
      "jid": "15550001@s.whatsapp.net",
      "optInSource": "checkout",
      "optInEvidenceReference": "consent-record-123",
      "optedInAt": "2026-07-01T10:00:00Z"
    }
  ]
}
```

Schedule and then explicitly start it:

```http
POST /campaigns/{campaignId}/schedule
{"startsAt":"2026-07-23T02:00:00Z"}

POST /campaigns/{campaignId}/start
```

Starting before `startsAt` is safe: recipient jobs remain ineligible until the
persisted due time.

## Read and control

```text
GET  /campaigns?status=running&limit=50&cursor=...
GET  /campaigns/{campaignId}
GET  /campaigns/{campaignId}/recipients?limit=50&cursor=...
GET  /campaigns/{campaignId}/audit?limit=50&cursor=...
POST /campaigns/{campaignId}/pause
POST /campaigns/{campaignId}/resume
POST /campaigns/{campaignId}/abort
```

List responses include an optional `meta.nextCursor`. Treat cursors as opaque
and use them only with the endpoint that returned them.

Invalid input and cursors return 400. Missing campaigns return 404. Invalid or
concurrent lifecycle transitions return 409 with code
`campaign_state_conflict`.

Clients can detect availability through the `campaign_orchestration` value in
`GET /server/capabilities`.
