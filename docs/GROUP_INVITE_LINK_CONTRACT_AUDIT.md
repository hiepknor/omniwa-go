# Group Invite-Link Contract Audit

Status: confirmed backend defect and contract ambiguity; fixed by the pull
request that adds this report.

## Root cause

The normalized detail and invite-link read were instance-scoped projection
reads, but before this fix they used different repository methods:

- `POST /group/info` enters `GetGroupInfo` at
  `pkg/group/handler/group_handler.go:373`, then `ManagementReader.Get` at
  `pkg/group/service/management_read.go:235`, and finally
  `groupRepository.GetManagement` at
  `pkg/projection/repository/group_repository.go:716`.
- `POST /group/invitelink` entered `GetGroupInviteLink` at
  `pkg/group/handler/group_handler.go:456`, then the service at
  `pkg/group/service/group_service.go:311`, `GroupReader.InviteLink` at
  `pkg/projection/service/group_reader.go:167`, and
  `groupRepository.GetInviteLink` at
  `pkg/projection/repository/group_repository.go:1123`.

Both repository methods bind `instance_id` and canonical `group_id` against
`projected_groups`. The invite-link query additionally excludes tombstoned
rows. Before this fix, `GroupReader.InviteLink` converted both a missing row and
a present row with a null or empty `invite_link` into the same `found=false`
result. The Group service then converted that result to
`gorm.ErrRecordNotFound`, and the shared error mapper emitted generic HTTP 404
`not_found: group projection record not found`.

Consequently, a usable Group detail followed by that error did not prove an
instance mismatch, bad JID, migration failure, or convergence race. The common
case was a valid Group row whose optional invite-link cache had never been
populated. The old error text was false and merged two materially different
states.

After the fix, normalized detail and invite-link reads both call
`ManagementReader.getRecord` at `pkg/group/service/management_read.go:256` and
`groupRepository.GetManagement` in one authoritative row lookup. The internal
`ManagementReader.InviteLink` method at
`pkg/group/service/management_read.go:244` computes the read decision and cache
availability from that same record. The older projection reader still
preserves missing-row errors for compatibility callers, but it is no longer the
normalized invite-link authority.

## Permission and availability

`managementActions` at `pkg/group/service/management_read.go:610` assigns both
invite-link actions the admin decision calculated at
`pkg/group/service/management_read.go:661`. These actions answer whether the
actor may attempt the operation against the projected Group state. They do not
assert that a cached invite link exists.

The contract now exposes `GroupDetail.inviteLink.available` separately. It is
true only when this projection row contains a non-empty cached link. False does
not mean that WhatsApp has no invite link. The action decisions remain advisory
and both the link read and reset mutation revalidate the same normalized Group
record. The read does so before returning the cached secret; the mutation does
so immediately before provider admission. Denied reads return
`group_permission_denied`, while permission that can no longer be established
returns `group_state_changed`.

## Projection serving behavior

Both read paths require the effective Groups state through `readMeta` at
`pkg/projection/service/group_reader.go:182` or the equivalent management read.
Groups schema version 4 is required. Ready, stale, and syncing states with a
prior reconciliation are readable and disclose their actual status in
projection metadata. Missing state, not-started, failed, schema older than 4,
or no prior reconciliation returns HTTP 503 `projection_not_ready` before a
Group-row lookup.

`group_management_permissions` is therefore not advertised for schema 3. It is
advertised for a serving schema-4 Groups projection only. No read path falls
back to live WhatsApp.

## Final public contract

`POST /group/invitelink` with `reset=false` keeps its existing successful
payload for generated-client compatibility:

```json
{
  "message": "success",
  "data": "https://chat.whatsapp.com/example",
  "meta": {
    "source": "projection",
    "syncStatus": "ready",
    "lastSyncedAt": "2026-07-28T10:00:00Z"
  }
}
```

An existing Group without a cached link returns:

```json
{
  "error": "cached group invite link is not available",
  "code": "group_invite_link_not_found",
  "requestId": "opaque-request-id",
  "details": {
    "available": false,
    "meta": {
      "source": "projection",
      "syncStatus": "ready",
      "lastSyncedAt": "2026-07-28T10:00:00Z"
    }
  }
}
```

The status is HTTP 404 because the requested subordinate resource, the cached
invite link, is absent. A missing instance-scoped Group row is separately HTTP
404 `group_not_found`. A non-serving projection is HTTP 503
`projection_not_ready`. Permission and state races on `reset=true` remain the
typed management-command errors `group_permission_denied`,
`group_state_changed`, or `projection_not_ready`.

The missing-cache result is deterministic for the current projection snapshot.
Repeating the same read may change only after reconciliation, an event, or a
confirmed reset writes a link. Clients must not use immediate retry as a live
provider probe and must not reset implicitly after a 404.

## Reset semantics

`ManagementCommandManager.ResetInviteLink` at
`pkg/group/service/management_commands.go:189` persists the command,
revalidates `actions.resetInviteLink`, applies rate protection, and admits one
provider call. `groupService.ResetManagementInviteLink` at
`pkg/group/service/group_service.go:845` now confirms synchronous projection
write-through through `GroupWriter.WriteInviteLink` at
`pkg/projection/service/group_writer.go:125` before a command can be recorded as
`completed`.

If the provider may have reset the link but write-through fails, the public
acknowledgement is `unknown`. It is audited without the link, provider payload,
credential, or idempotency key. The backend does not retry it. Reusing the same
`Idempotency-Key` returns the stored outcome and does not call the provider
again.

## Compatibility, rollout, and rollback

This fix is additive: successful `data` remains a string, projection metadata
is added to the envelope, Group detail gains an availability object, and a
previously generic 404 receives stable domain codes. There is no migration and
no new capability. Deploy behind the existing
`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED` gate after Groups schema 4 is serving.

Rollback is disabling that gate or reverting the application commit. There is
no data rollback. Invite-link values written by confirmed resets remain valid
projection data.
