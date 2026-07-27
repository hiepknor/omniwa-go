# Group Lists

Group Lists are reusable, named sets of WhatsApp group JIDs owned by one
instance. They are the server-managed target source for group campaigns. A list
entry represents one group; OmniWA GO never expands it into group members.

## Availability and authorization

The routes are disabled by default. Operators enable the complete stack with:

```env
WA_GROUP_LISTS_ENABLED=true
```

Clients must still check `GET /server/capabilities`. The `group_lists`
capability appears only when the feature is enabled and that instance's groups
projection is ready at the required schema version. Use an instance token in
the `apikey` header for every Group List route.

Create and update require an authorization assertion. OmniWA GO stores only a
list-scoped SHA-256 hash of `evidenceReference`; it never stores or returns the
raw reference. The caller remains responsible for the legal and operational
sufficiency of that assertion.

## Create a list

```http
POST /group-lists
apikey: <instance-token>
Content-Type: application/json
```

```json
{
  "name": "Northern branches",
  "description": "Operational branch groups",
  "groupJids": [
    "120363000001@g.us",
    "120363000002@g.us"
  ],
  "authorization": {
    "source": "operator_attestation",
    "evidenceReference": "approval-ticket-123",
    "authorizedAt": "2026-07-26T10:00:00Z"
  }
}
```

Lists must be non-empty, contain no duplicate JIDs, and contain only canonical
`@g.us` identities. Every group must be eligible when the list is created or
updated. Unknown projection state returns `503 projection_not_ready`; a known
but unavailable group returns `409 group_list_group_unavailable`.

## Read eligibility

```text
GET /group-lists?search=north&limit=50&cursor=...
GET /group-lists/{groupListId}
GET /group-lists/{groupListId}/groups?limit=50&cursor=...
```

Each entry response keeps the stored name separate from the current projection
and includes the backend's permission decision:

```json
{
  "groupJid": "120363000001@g.us",
  "snapshotName": "Branch 01",
  "currentName": "HCM Branch",
  "eligibility": "eligible",
  "eligibilityReason": null,
  "canSend": true,
  "checkedAt": "2026-07-26T10:00:00Z"
}
```

Eligibility is `eligible`, `unavailable`, or `unknown`. Stable reasons are
`group_access_lost`, `group_dissolved`, `send_permission_denied`,
`group_suspended`, and `projection_not_ready`. Clients must not infer send
permission from group metadata. A provider-side rename changes `currentName`
but does not change the list version or `snapshotName`.

## Replace, delete, and audit

Updates are full replacements and require the version last read by the client:

```http
PUT /group-lists/{groupListId}
```

```json
{
  "name": "Northern branches",
  "description": "Updated scope",
  "groupJids": ["120363000001@g.us"],
  "expectedVersion": 3,
  "authorization": {
    "source": "operator_attestation",
    "evidenceReference": "approval-ticket-456",
    "authorizedAt": "2026-07-26T11:00:00Z"
  }
}
```

A stale version returns `409 group_list_version_conflict`. Deletion is soft and
does not remove audit records or alter campaign snapshots created from the
list.

```text
DELETE /group-lists/{groupListId}
GET    /group-lists/{groupListId}/audit?limit=50&cursor=...
```

List, entry, and audit cursors are opaque and scoped to their instance,
endpoint, parent list, and search term. Do not reuse a cursor in another query.

## Rollout and rollback

Apply the additive migration while `WA_GROUP_LISTS_ENABLED=false`, validate the
projection and PostgreSQL checks, and then enable the flag. For application
rollback, disable the flag or deploy the previous image. The tables and audit
history are intentionally retained; schema defects are corrected with a
forward migration rather than a destructive down migration.
