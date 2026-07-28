# OmniWA Console compatibility handoff

This document is the implementation contract for adopting the current OmniWA
GO backend from OmniWA Console. It is intentionally capability-driven: Console
must continue to operate against an older backend while the new backend lands,
and the backend must retain the old public contract until measured Console
adoption permits its removal.

The generated OpenAPI files in [`docs/`](./) are the field-level source of
truth. This handoff defines rollout order, compatibility behavior, and release
gates; it does not duplicate every request or response schema.

## Non-negotiable client rules

1. Call `GET /server/capabilities` after authentication and cache the result for
   the current backend origin and authenticated scope. Refresh it after login,
   instance selection, reconnect, or backend revision change. A supported old
   backend that returns 404 for this endpoint is equivalent to an empty
   capability set, not an application failure.
2. Branch on capability names, HTTP status, and stable `code` fields. Never
   branch on human-readable `error` text.
3. Treat `meta`, `code`, `retryAfter`, `requestId`, and `credentialVersion` as
   additive fields. Unknown response fields and unknown capabilities must be
   ignored safely.
4. Treat cursors as opaque and scope-bound. Never parse, persist indefinitely,
   synthesize, or reuse a cursor with a different instance, filter, search, or
   resource.
5. Do not use provider-native payloads, database fields, WebSocket arrival
   order, or error strings as compatibility interfaces.
6. Do not perform live WhatsApp information queries merely to refresh a list or
   dashboard when the matching projection capability is present.
7. Never log, send to analytics, place in a URL, or persist an instance token in
   ordinary application state. If Console requires the credential for transport,
   keep it only in the approved secret boundary (for example, a server-side
   secret store or protected session), not a client-visible application store.

## Capability and endpoint matrix

`GET /server/capabilities` accepts either an admin key or an instance token and
returns `data.version`, `data.revision`, and `data.capabilities`. Projection
capabilities are instance-specific and appear only after that instance has a
serving projection at the required schema version. Administrative capabilities
are returned only to an admin-authenticated request.

| Capability | Console behavior when present | Primary endpoints |
|---|---|---|
| `rate_limit_retry_after` | Parse public 429 responses and honor `Retry-After` | Existing information-query endpoints |
| `groups_projection` | Use projection-backed groups; do not fan out live refreshes | `GET /group/list`, `GET /group/search`, `POST /group/info` |
| `group_management_permissions` | Use normalized Group summaries/detail and tri-state advisory action decisions; never infer permissions from members | `GET /group/list`, `GET /group/search`, `POST /group/info` |
| `group_members_projection` | Use the projection-backed member directory with opaque member references and advisory per-member actions | `GET /group/{groupJid}/members` |
| `group_management_commands` | Use strict journaled mutations, typed acknowledgements/outcomes, command-time permission checks, and `Idempotency-Key` | Existing `/group/*` mutation routes except photo |
| `group_management_audit` | Show bounded public-safe terminal management history | `GET /group/{groupJid}/audit` |
| `group_photo_assets` | Upload a private image asset, then set the Group photo by opaque asset ID | `POST /media-assets`, `POST /group/photo` |
| `labels_projection` | Use persisted label list/detail reads | `GET /label/list`, `GET /label/info/{labelId}` |
| `contacts_projection` | Use normalized persisted contacts for list/search/detail | `GET /user/contacts`, `GET /user/contacts/search`, `GET /user/contact/{contactId}` |
| `chats_projection` | Use cursor-paged chat reads | `GET /chat/list`, `GET /chat/info/{chatId}` |
| `messages_projection` | Use cursor-paged history and persisted delivery state | `GET /chat/{chatId}/messages`, `GET /message/{messageId}`, `GET /message/{messageId}/delivery` |
| `events_projection` | Use durable, retention-bound event history | `GET /events` |
| `outbound_rate_limit` | Parse outbound pacing errors independently from information-query limits | Existing `/send/*` mutations |
| `campaign_orchestration` | Use server-owned campaign state and recipient jobs | `/campaigns` and its control/history endpoints |
| `group_lists` | Use server-owned, versioned group target lists and backend eligibility | `/group-lists` and its group/audit endpoints |
| `group_list_eligibility` | Offer advisory ordered batch and current-list aggregate preflight; consume backend decisions only | `POST /group-lists/eligibility`, `GET /group-lists/{groupListId}/eligibility` |
| `projection_failure_operations` | Show admin projection-failure operations | `/server/projection-failures*` |
| `instance_metadata_views` | Use credential-free instance list/detail contracts | `GET /instance/metadata`, `GET /instance/metadata/{instanceId}` |
| `instance_token_rotation` | Offer compare-and-swap token rotation | `POST /instance/rotate-token/{instanceId}` |
| `instance_credential_health` | Show secret-free migration facts to admins | `GET /instance/credential-health` |

Absence of a projection capability does not mean a valid empty collection. It
means Console must use its legacy-compatible behavior or show a syncing/not
available state. Console must not manufacture `[]` while the projection is not
ready.

## Group management read cutover

`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED=false` preserves the provider-shaped
Group read responses for rollback. When the flag is enabled, the server only
advertises `group_management_permissions` after the Groups projection is
serving schema version 4. Console must switch the three existing Group read
routes to their normalized DTOs only when that capability is present.

`GET /group/search` accepts `q`, `type`, `myRole`, `sendMode`, `state`,
`membershipState`, `limit`, and `cursor`. `GET /group/list` is the unfiltered,
paginated form. Both return `GroupSummary` rows and never return participants.
The default page size is 50 and the maximum is 200. Cursors are opaque and
bound to the instance and every filter. A changed scope returns
`invalid_cursor`; an unknown enum returns `invalid_filter`.

`POST /group/info` keeps its existing `{ "groupJid": "...@g.us" }` request and
returns `GroupDetail` without an embedded member list. Its `actions` entries
are advisory decisions with `state=allowed|denied|unknown`, a public-safe
reason, and `checkedAt`. `unknown` is a first-class result: Console must disable
the action and explain that permission cannot currently be established. It
must never turn missing facts into either an allow or a denial. Every mutation
will independently revalidate current permission and group state in the
command stage.

The read model does not currently prove that a community parent is a sendable
chat, so `actions.sendMessage` is `unknown` with reason `unsupported` for
`type=community`. Console must target a supported subgroup instead of treating
the community container as a chat.

The backend resolves the current instance through persisted Phone, LID, and
JID aliases. A positive projected participant match can establish membership
and role. An incomplete alias graph or missing participant cannot establish
`not_member`; the response remains `unknown` unless the projection contains an
explicit `left` or `removed` membership state. Owner references are opaque
member IDs, not provider aliases.

`GET /group/{groupJid}/members` is available only with
`group_members_projection`. It accepts `q`, `role`, `limit`, and `cursor`, uses
the same 50/200 page limits, and never calls WhatsApp. Search is a
case-insensitive display-name prefix search. Rows expose only an opaque UUID
`memberId`, optional display name, normalized role and membership state, plus
tri-state `promote`, `demote`, and `remove` decisions. They do not expose Phone,
LID, or provider JID aliases. The cursor is bound to the instance, group, query,
and role filter. Per-member decisions are UI preflight only; command handlers
must resolve the opaque reference and revalidate actor and target state before
provider admission.

Deployment order for this read stage is:

1. Deploy the backend with the flag disabled and allow reconciliation to
   publish Groups schema version 4.
2. Deploy Console support for both the legacy and normalized response shapes.
3. Enable the flag for a canary instance and require
   `group_management_permissions` before using the normalized path. Require
   `group_members_projection` separately before loading the member directory.
4. Monitor `projection_not_ready`, invalid cursor/filter responses, and the
   proportion of `unknown` action decisions before broader rollout.

Disable the flag to restore the legacy read behavior. The additive projection
columns and schema version remain in place; no data rollback is required.

## Group management commands and audit

When both `group_management_permissions` and `group_management_commands` are
present, Console may use the normalized mutation contract on the existing
paths. Requests use the instance token in `apikey`, strict JSON, a maximum
64 KiB body, and an optional `Idempotency-Key` of at most 255 bytes. Reusing a
key with different input returns `idempotency_conflict`. Console must not retry
an `unknown` outcome automatically.

Name, description, settings, invite-link reset, and leave return a
`CommandAcknowledgement`. `completed` means the provider command returned a
confirmed success; `unknown` means the final provider outcome could not be
established. `projectionRefreshExpected` does not claim projection convergence.
All commands revalidate the current projected actor permission and group state
immediately before provider admission.

`POST /group/participant` accepts 1-100 unique participants. `add` uses
canonical `@s.whatsapp.net` JIDs; `remove`, `promote`, and `demote` use the
opaque `memberId` returned by the member directory. The ordered response has a
separate `succeeded`, `failed`, or `unknown` outcome per requested participant.
Outcomes preserve request order, but `participant` is an opaque public
reference and must not be treated as a provider alias. Only confirmed successes
are written through to projection.

`POST /group/create` accepts 1-100 canonical, unique user JIDs and returns
bounded participant outcomes, including partial and unknown results.
`POST /group/join` never infers membership from provider acknowledgement alone:
it returns `joined` only after post-command confirmation, otherwise a stable
rejected reason or `unknown`. Create and join may have no resolved Group JID
while their command is in progress.

HTTP 429 responses include `Retry-After`; Console must delay the operator action
and must not submit an automatic mutation retry. HTTP 403
`group_permission_denied`, HTTP 409 `group_state_changed`, and HTTP 503
`projection_not_ready` are expected race-safe command-time rejections.

When `group_management_audit` is present,
`GET /group/{groupJid}/audit?limit=&cursor=` returns terminal events newest
first. The default limit is 50 and maximum is 200. Its cursor is opaque and
bound to the instance and Group JID. Audit summaries are deliberately bounded
and never contain participant identities, invite links, media, credentials,
idempotency keys, or provider payloads.

Rollout commands/audit only after migrations 30-32 are applied, Groups schema
version 4 is serving, and Console recognizes both capabilities. Disable
`WA_GROUP_MANAGEMENT_CONTRACT_ENABLED` to restore legacy route behavior; retain
the append-only journal and audit data during rollback.

## Shared-media Group photos

When `group_photo_assets` is present, Console must first upload a JPEG or PNG
from the operator device through `POST /media-assets`, then call:

```json
{
  "groupJid": "120363000001@g.us",
  "mediaAssetId": "927beb51-46c2-4331-b3b4-d96f67280bd3"
}
```

The Group photo route does not accept a URL, data URL, base64 image, filename,
or object-storage reference in this mode. The asset must be ready and owned by
the authenticated instance. The backend validates the canonical image, applies
a bounded square normalization, revalidates `actions.setPhoto`, journals the
command, and calls the provider once. `completed` confirms the provider call;
`unknown` must be shown for review and must not be retried automatically.

`GroupDetail.photo.mediaAssetId` is an opaque safe reference when the current
photo was set through this contract. Console may retrieve its canonical bytes
only through authenticated `GET /media-assets/{mediaId}/content`; it must not
construct a storage URL. An absent asset ID does not prove there is no provider
photo, so `photo.available` remains a separate nullable fact.

Deploy migration 33 with `WA_GROUP_PHOTO_ASSETS_ENABLED=false`, verify the
private shared bucket and device upload path, then enable the Group management
contract and photo gate for a canary. Disable only the photo gate to restore the
legacy photo request path. Existing private objects and durable references must
remain in place during rollback.

## Shared response behavior

Projection-backed success responses preserve their existing `message` and
`data` fields and may add:

```json
{
  "meta": {
    "source": "projection",
    "syncStatus": "ready",
    "lastSyncedAt": "2026-07-23T00:00:00Z",
    "nextCursor": "opaque-value"
  }
}
```

Console must distinguish these cases:

| Condition | Required UI behavior |
|---|---|
| HTTP 200, empty `data`, serving projection | Render a valid empty state |
| HTTP 200, `meta.syncStatus=syncing` | Render available data with a non-blocking syncing indicator |
| HTTP 200, `meta.syncStatus=stale` | Render available data with a stale-data warning and timestamp |
| HTTP 503, `code=projection_not_ready` | Render a retryable synchronization state, not an empty state |
| HTTP 400, `code=invalid_cursor` | Discard the current cursor chain and restart from the first page once |
| HTTP 429, `code=rate_limited` | Pause that operation for `Retry-After` seconds; do not spin or fan out retries |
| HTTP 429, `code=outbound_rate_limited` | Pause the outbound action independently; do not treat it as projection throttling |
| HTTP 500 | Show the public-safe message and retain `requestId` for support; never expect internal details |

The information-query 429 body remains backward compatible because `error` is
a string. `code` and `retryAfter` are additive; the `Retry-After` header is the
authoritative delay when present.

## Credential migration contract

Instance list/info/create responses currently retain the legacy `token` field
for rollback compatibility. Console must stop reading that field before the
backend removes it.

### Console implementation

1. When `instance_metadata_views` is present, use `GET /instance/metadata` and
   `GET /instance/metadata/{instanceId}` for ordinary list/detail screens. On a
   supported old backend, retain the legacy paths but discard `token` at the
   transport boundary. Remove it from view models, UI rendering, stores, query
   caches, logs, analytics, crash reports, and persistence.
2. Continue accepting `credentialVersion` as optional while old backends exist.
3. Treat the token returned by instance creation as a one-time secret: display
   it only in a dedicated confirmation step, require the operator to copy or
   download it, and clear it on navigation or dismissal.
4. When `instance_token_rotation` is present, submit the currently displayed
   `credentialVersion` as `expectedVersion` plus a bounded operator reason.
   Treat the returned token as one-time and replace the stored integration
   credential immediately.
5. On `409 credential_version_conflict`, discard the attempted result, refresh
   instance metadata, and require an explicit new rotation attempt. Never retry
   rotation automatically.
6. Do not send tokens to telemetry. Redact `apikey` request headers in browser,
   BFF, proxy, and observability tooling.

### Admin migration health

When `instance_credential_health` is present, an admin request to
`GET /instance/credential-health` returns:

```json
{
  "message": "success",
  "data": {
    "generatedAt": "2026-07-23T00:00:00Z",
    "currentKeyVersion": 1,
    "instances": {
      "total": 10,
      "currentDigest": 10,
      "plaintextOnly": 0,
      "otherKeyVersion": 0
    },
    "plaintextFallback": {
      "lookups": 3,
      "affectedInstances": 2,
      "firstObservedAt": "2026-07-20T00:00:00Z",
      "lastObservedAt": "2026-07-20T01:00:00Z"
    }
  }
}
```

These are lifetime facts, not a backend `safeToRemove` decision. Console may
display them but must not infer safety from `plaintextOnly == 0` alone.

## Required rollout sequence

### C0: Compatibility adapter

- Add typed capability discovery keyed by backend revision and auth scope.
- Centralize safe error parsing and `Retry-After` handling.
- Centralize projection metadata and opaque cursor handling.
- Add contract fixtures for old responses without additive fields and new
  responses with them.

Exit gate: the same Console build works against the supported old backend and
the current backend without using error strings or unknown payload fields.

### C1: Projection-backed screens

- Move Groups first, then Labels/Contacts, then Chats/Messages/Events.
- Gate each screen independently on its capability; do not use one projection
  capability as evidence that another resource is ready.
- Remove tab-level refresh fan-out and deduplicate query ownership in the
  Console data layer.
- Use cursor pagination only on projection endpoints and reset the cursor chain
  when filters or instance identity change.
- Surface syncing, stale, failed, and throttled states separately.

Exit gate: repeated refreshes and multiple open tabs do not increase live
WhatsApp information-query counts for projection-backed reads.

### C2: Credential-safe Console

- Stop consuming the legacy token field on list/info paths.
- Prefer the credential-free metadata endpoints whenever their capability is
  present; test the old-backend discard-at-boundary fallback separately.
- Implement one-time create/rotate secret UX and conflict handling.
- Verify browser storage, state snapshots, logs, analytics, and error reporting
  contain no token.
- Optionally expose the admin credential-health facts without a safety verdict.

Exit gate: automated tests prove ordinary Console operation remains functional
when list/info responses omit `token` entirely.

### C3: Measured observation window

- Deploy C2 to every supported Console environment before starting the window.
- Record the Console release identifier and deployment completion timestamp.
- Monitor `GET /instance/credential-health` and backend health throughout a
  separately approved rollback window.
- Investigate every fallback observed after the Console deployment timestamp;
  a new fallback restarts the quiet-window clock.
- Require `currentDigest == total`, `plaintextOnly == 0`, and
  `otherKeyVersion == 0` throughout the final gate.
- Exercise backup/restore and token-rotation recovery before approving the
  destructive backend migration.

Exit gate: the product owner, Console owner, backend owner, security owner, and
operations owner record explicit approval with evidence. No code path should
derive approval automatically.

### C4: Backend contract cleanup

Only after C3 may OmniWA GO open separate, reversible-first PRs to:

1. stop returning `token` from ordinary instance list/info responses;
2. verify the deployed Console and integrations remain healthy;
3. remove plaintext token storage in a later migration after the agreed
   rollback/recovery checkpoint.

Creation and rotation must continue to return the new token exactly once.

## Acceptance matrix for OmniWA Console

The Console PRs must cover at least:

- old capability response and current capability response;
- admin-scoped versus instance-scoped capabilities;
- valid empty projection versus `projection_not_ready`;
- `ready`, `syncing`, and `stale` metadata;
- invalid/expired cursor recovery without an infinite loop;
- 100 identical UI refresh intents for one query key collapsing to bounded
  client requests;
- information-query and outbound 429 handling with independent timers;
- create/rotate one-time secret lifecycle and page-navigation cleanup;
- rotation conflict and network ambiguity without automatic resubmission;
- list/info fixtures with `token`, without `token`, and with unknown additive
  fields;
- metadata list/detail fixtures proving credential fields are absent;
- log, analytics, browser-storage, and state-snapshot secret scans.

## Rollback

Console rollout is additive and should be independently feature-flagged by
resource. A Console rollback may disable a new screen or adapter, but it must
not restore token display, token persistence, retry storms, or live-query fan
out. Backend plaintext removal has no authorization from this handoff; its own
future change requires the C3 evidence and a separate migration/rollback plan.

## Handoff completion record

Attach the following to the Console release ticket:

- Console commit and immutable artifact digest;
- supported backend revision range;
- capability/contract test results;
- secret-scan evidence;
- deployment completion timestamp for every environment;
- observation-window start, restarts, and end;
- credential-health snapshots at start and approval;
- backup/restore drill reference;
- named approvals for C4.
