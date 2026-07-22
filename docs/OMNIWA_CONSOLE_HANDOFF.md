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
| `labels_projection` | Use persisted label list/detail reads | `GET /label/list`, `GET /label/info/{labelId}` |
| `contacts_projection` | Use normalized persisted contacts for list/search/detail | `GET /user/contacts`, `GET /user/contacts/search`, `GET /user/contact/{contactId}` |
| `chats_projection` | Use cursor-paged chat reads | `GET /chat/list`, `GET /chat/info/{chatId}` |
| `messages_projection` | Use cursor-paged history and persisted delivery state | `GET /chat/{chatId}/messages`, `GET /message/{messageId}`, `GET /message/{messageId}/delivery` |
| `events_projection` | Use durable, retention-bound event history | `GET /events` |
| `outbound_rate_limit` | Parse outbound pacing errors independently from information-query limits | Existing `/send/*` mutations |
| `campaign_orchestration` | Use server-owned campaign state and recipient jobs | `/campaigns` and its control/history endpoints |
| `projection_failure_operations` | Show admin projection-failure operations | `/server/projection-failures*` |
| `instance_metadata_views` | Use credential-free instance list/detail contracts | `GET /instance/metadata`, `GET /instance/metadata/{instanceId}` |
| `instance_token_rotation` | Offer compare-and-swap token rotation | `POST /instance/rotate-token/{instanceId}` |
| `instance_credential_health` | Show secret-free migration facts to admins | `GET /instance/credential-health` |

Absence of a projection capability does not mean a valid empty collection. It
means Console must use its legacy-compatible behavior or show a syncing/not
available state. Console must not manufacture `[]` while the projection is not
ready.

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
