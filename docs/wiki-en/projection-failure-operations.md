# Projection failure operations

Projection workers use bounded retries and move poison events to a dead-letter
state. Administrators can inspect and resolve those failures without reading
raw event payloads or querying WhatsApp.

The feature is available when the admin-scoped `GET /server/capabilities`
response includes `projection_failure_operations`. All endpoints below require
the global admin key in the `apikey` header; instance tokens are not accepted
and do not receive this capability.

## Inspect dead letters

```http
GET /server/projection-failures?instanceId=<uuid>&resource=groups&limit=50
```

`instanceId` and `resource` are optional filters. `limit` must be between 1 and
200. Follow `data.nextCursor` using the `cursor` query parameter. Cursors are
opaque and bound to the original filter scope.

Items contain operational fields such as instance, resource, event key, event
type, safe error code, retry count, and timestamps. They never contain the
normalized payload or entity identifier.

## Replay after a fix

Replay only after the responsible projector, schema, or data issue is fixed:

```http
POST /server/projection-failures/replay
Content-Type: application/json
apikey: <global-admin-key>

{
  "instanceId": "<uuid>",
  "resource": "groups",
  "eventKey": "<event-key>",
  "reason": "projector fix deployed in revision abc123"
}
```

Replay resets the retry budget and makes the event pending immediately. It does
not synchronously process the event or call WhatsApp.

## Discard an obsolete event

```http
POST /server/projection-failures/discard
Content-Type: application/json
apikey: <global-admin-key>

{
  "instanceId": "<uuid>",
  "resource": "groups",
  "eventKey": "<event-key>",
  "reason": "event superseded by a verified reconciliation snapshot"
}
```

Discard is terminal. Use it only when ignoring the event is an explicit data
decision, not as a substitute for fixing a projector. Reasons are retained in
the audit table: keep them factual and do not include credentials, message
content, phone numbers, or other sensitive data.

Audit records include the response `X-Request-ID` and a domain-separated hash
representing the authenticated admin credential. The raw credential is never
stored. Retain the request ID in operational change records when possible.

Both mutation endpoints return HTTP 404 when the identity does not exist and
HTTP 409 when another worker or administrator has already transitioned it.
Retries after network uncertainty are safe: a repeated operation receives the
409 response and cannot create a second audit action.
