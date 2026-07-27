# Campaign orchestration

Campaigns are durable, instance-scoped text delivery jobs. Direct campaigns
use persisted recipient consent. The campaign contract also reserves a
server-snapshotted Group List target for the staged group-delivery rollout.
Lifecycle controls, audit history, and progress are owned by the backend.

## Safety contract

- Use an instance token in the `apikey` header. The global admin key is not
  accepted by campaign routes.
- A direct campaign recipient must be a direct WhatsApp JID and include
  `optInSource`, `optInEvidenceReference`, and `optedInAt`.
- A group campaign accepts one `group_list` target, never a caller-supplied
  array of group JIDs. One list entry becomes one group target; members are not
  expanded.
- OmniWA GO hashes evidence references before persistence. It does not verify
  that the caller's consent assertion is legally or operationally sufficient.
- Drafts are limited to 10,000 recipients and request bodies to 8 MiB.
- Pause or abort stops new claims. A recipient already leased by a worker may
  still finish.
- Delivery is at-least-once across the external provider boundary. Stable
  message IDs reduce duplicate risk but do not establish exactly-once delivery.

## Create and activate

Create a legacy-compatible direct draft:

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

The additive Group List request shape is:

```json
{
  "name": "July campaign",
  "text": "Campaign content",
  "target": {
    "type": "group_list",
    "groupListId": "4cae2734-b8f4-4faa-8d09-5933ef3bf1b0",
    "groupListVersion": 4
  }
}
```

Group target creation remains intentionally disabled in this schema/contract
release. It returns `409 campaign_group_targets_disabled` until the group-aware
worker, revalidation, pacing, retry, and circuit-breaker slice is deployed.
The server does not advertise `campaign_group_targets` before that complete
stack is ready.

When enabled, creation locks the Group List, checks instance scope, version,
non-empty membership, and backend eligibility, and snapshots the list ID, list
name, list version, group JIDs, and current group labels in one transaction.
Renaming, updating, or deleting the Group List afterward does not change the
campaign snapshot.

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
and use them only with the endpoint that returned them. Both the campaign list
and detail endpoints return the same backend-defined progress fields:

```json
{
  "progress": {
    "total": 120,
    "processed": 72,
    "pending": 47,
    "processing": 1,
    "sent": 68,
    "delivered": 0,
    "read": 0,
    "failed": 2,
    "skipped": 2,
    "aborted": 0,
    "updatedAt": "2026-07-26T10:00:00Z"
  },
  "statusReason": null,
  "pauseReason": null,
  "retryAt": null,
  "needsAttention": false
}
```

`processed` is the sum of `sent`, `delivered`, `read`, `failed`, `skipped`,
and `aborted`; the Console must not infer a different set of terminal states.
For group targets, `sent` means provider acknowledgement for the group message.
It does not imply delivery or reading by every group member.

Invalid input and cursors return 400. Missing campaigns return 404. Invalid or
concurrent lifecycle transitions return 409 with code
`campaign_state_conflict`.

Clients can detect availability through the `campaign_orchestration` value in
`GET /server/capabilities`.

## Staged rollout and rollback

Migration 22 is additive. Existing campaigns and recipients are backfilled by
the database defaults as `targetType=direct`; older direct history remains
readable and auditable. The default `WA_CAMPAIGN_DIRECT_CREATE_ENABLED=true`
preserves the existing create API during the Console migration. Set it to
`false` only after clients have moved to Group Lists, or to stop new direct
campaign creation without affecting existing history.

This release does not expose a group-execution feature flag or capability, so
it cannot enqueue group work accidentally. Application rollback deploys the
previous image and leaves the additive columns unused. Do not remove migration
22 columns during the rollback window. After group execution is enabled in a
later release, disable new group creation and drain or pause group campaigns
before rolling back to a binary that is not group-aware. See
[ADR 0022](../adr/0022-group-list-campaign-execution.md) for the full rollout
and recovery rules.
