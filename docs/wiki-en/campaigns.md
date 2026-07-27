# Campaign orchestration

Campaigns are durable, instance-scoped text or image delivery jobs. Every new
campaign uses a server-snapshotted Group List and one delivery target per group.
Legacy direct campaigns remain readable, auditable, and executable.
Lifecycle controls, audit history, and progress are owned by the backend.

## Safety contract

- Use an instance token in the `apikey` header. The global admin key is not
  accepted by campaign routes.
- A group campaign accepts one `group_list` target, never a caller-supplied
  array of group JIDs. One list entry becomes one group target; members are not
  expanded.
- OmniWA GO hashes evidence references before persistence. It does not verify
  that the caller's consent assertion is legally or operationally sufficient.
- Drafts are limited to 10,000 recipients and request bodies to 8 MiB.
- Image content references one ready private media asset and permits an optional
  caption of at most 1,024 Unicode characters. The image bytes are never placed
  in campaign JSON.
- Pause or abort stops new claims. A recipient already leased by a worker may
  still finish.
- Delivery is at-least-once across the external provider boundary. Stable
  message IDs reduce duplicate risk but do not establish exactly-once delivery.

## Create and activate

Create a Group List draft:

```http
POST /campaigns
apikey: <instance-token>
Content-Type: application/json
```

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

New clients should use the typed content object. Text remains compatible:

```json
{
  "name": "July campaign",
  "content": {
    "type": "text",
    "text": "Campaign content"
  },
  "target": {
    "type": "group_list",
    "groupListId": "4cae2734-b8f4-4faa-8d09-5933ef3bf1b0",
    "groupListVersion": 4
  }
}
```

For an image uploaded through `/campaign-media`, use its opaque ID and an
optional caption:

```json
{
  "name": "Branch image update",
  "content": {
    "type": "image",
    "mediaId": "927beb51-46c2-4331-b3b4-d96f67280bd3",
    "caption": "Campaign content"
  },
  "target": {
    "type": "group_list",
    "groupListId": "4cae2734-b8f4-4faa-8d09-5933ef3bf1b0",
    "groupListVersion": 4
  }
}
```

Do not send both legacy top-level `text` and `content`. All new campaigns
require a Group List target; direct-recipient creation is rejected with
`409 campaign_direct_create_disabled`. Creation and
lifecycle transitions return `409 campaign_image_content_disabled` while
`WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=false`.

Creation locks the ready media asset in the authenticated instance and
snapshots its ID, decoded MIME type, normalized size, dimensions, SHA-256, and
caption in the same transaction as the target snapshot. Later cleanup or a
delete request cannot remove referenced media. Object keys and URLs never
appear in campaign responses or audit records.

The image worker reloads and verifies the private object before every provider
attempt. Storage reads and the provider media upload are transient, bounded
retry points. The provider message-send call has no internal retry. A send that
returns without a trustworthy acknowledgement becomes `unknown_send_outcome`,
pauses the campaign, and requires operator review instead of risking a duplicate
group message.

Group target creation and execution are controlled by
`WA_CAMPAIGN_GROUP_TARGETS_ENABLED`, which defaults to `false`. When the flag or
Group Lists are disabled, creation returns
`409 campaign_group_targets_disabled` and the server does not advertise
`campaign_group_targets`.

Image delivery additionally requires private MinIO storage and
`WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=true`. Enabling the image flag without both
Group List flags fails startup. The `campaign_image_content` capability is
advertised only when the flag is enabled and the groups projection is
serving-ready.

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

At start, unavailable targets are skipped and eligible targets continue. If no
target remains eligible, the API returns `409 campaign_no_eligible_targets`. An
unknown or stale group projection returns `503 projection_not_ready` without a
partial start.

The worker revalidates eligibility after claim and again at the provider
boundary. Terminal group errors are not retried. Known transient failures use
bounded exponential backoff with jitter. A provider rate limit opens an
instance-wide durable circuit and honors `Retry-After`. While that circuit is
open, it prevents both group and legacy direct campaign claims for the instance,
and progress exposes the circuit retry time. An unknown send outcome fails the
target, pauses the campaign, and sets `needsAttention=true` instead of risking a
duplicate send. Per-group leases and cooldowns prevent concurrent or
too-frequent sends across campaigns.

Backend safety policy is configured with
`WA_CAMPAIGN_GROUP_COOLDOWN`, `WA_CAMPAIGN_CIRCUIT_DURATION`,
`WA_CAMPAIGN_RATE_PAUSE_THRESHOLD`, and
`WA_CAMPAIGN_FAILURE_PAUSE_THRESHOLD`. Clients must display the returned pause,
retry, and attention fields rather than duplicate these thresholds.

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

Clients can detect the base API through `campaign_orchestration` and group
execution through `campaign_group_targets` in `GET /server/capabilities`.

## Staged rollout and rollback

Migrations 22 through 25 are additive. Existing campaigns and recipients are backfilled by
the database defaults as `targetType=direct`; older direct history remains
readable and auditable. Migration 23 adds durable group delivery guards,
instance circuit state, and aggregate safety counters. New direct-recipient
creation is disabled by default. Existing direct history remains readable,
auditable, and executable. `WA_CAMPAIGN_DIRECT_CREATE_ENABLED=true` is an
explicit emergency rollback switch for the retired create shape; enabling it
does not change the preferred Group List contract.

Keep `WA_CAMPAIGN_GROUP_TARGETS_ENABLED=false` through migration and image
deployment, then enable it in a canary environment. Disabling the flag removes
the capability, blocks new Group List campaigns, and stops new group claims;
existing direct work remains compatible. Do not remove migrations 22 through 25
structures during the rollback window. Before rolling back to a binary that is
not group-aware, disable the flag, pause every non-terminal group campaign, and
wait for or fence all active group leases. See
[ADR 0022](../adr/0022-group-list-campaign-execution.md) for the full rollout
and recovery rules.
