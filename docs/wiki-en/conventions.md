# API Conventions

Shared rules that apply across every endpoint. Individual request/response
schemas live in [Swagger UI](http://localhost:8080/swagger/index.html).

## Response envelope

Endpoints that return data use a `{ message, data }` envelope:

```json
{ "message": "success", "data": { "...": "endpoint-specific payload" } }
```

Action endpoints that have nothing to return (disconnect, logout, delete,
reconnect, …) omit `data`:

```json
{ "message": "success" }
```

In the OpenAPI spec the envelope is modelled with `apidocs.SuccessResponse`
composed with a concrete `data` type per endpoint (e.g. `data=types.GroupInfo`),
so Swagger shows the exact `data` shape rather than a generic object.

A handful of endpoints return their payload **directly, without the envelope**:
`GET /instance/logs/{id}` (array of log entries), `GET /label/list` (array of
labels), `GET /polls/{pollMessageId}/results` (results object), and the
advanced-settings routes (the settings object). The Swagger schema for each
endpoint is authoritative.

## Errors and request identities

Every HTTP response includes an `X-Request-ID` header. A caller may provide an
identity containing 16–64 ASCII letters, digits, dots, underscores, or hyphens;
the server replaces missing or unsafe values. Include this identity in support
and operational reports.

Failures preserve the legacy string `error` field and may add stable `code` and
`requestId` fields:

```json
{
  "error": "internal server error",
  "code": "internal_error",
  "requestId": "01234567-89ab-cdef-0123-456789abcdef"
}
```

The additive fields are optional while older validation paths are migrated.
Clients must not parse or branch on human-readable error text. Prefer the HTTP
status and then `code` when present. Internal error details are logged against
the request identity and are never returned in an HTTP 500 body.

| Status | Meaning |
|---|---|
| `400 Bad Request` | Validation failed — missing/invalid field, or a malformed phone/JID. The `error` string says which. |
| `401 Unauthorized` | Missing or wrong `apikey` (or, on `/ws`, a bad token). |
| `404 Not Found` | The instance or resource does not exist. |
| `500 Internal Server Error` | Unexpected server/provider error; the public message is generic and `code` is `internal_error`. Use `requestId` for investigation. |

Always branch on the HTTP status first, then on `code` when present, and surface
`error` to the user only as display text.

## Phone numbers and JIDs

Most messaging endpoints accept a `number` field. You may pass:

- a **plain phone number** in international format without `+`, e.g.
  `5511999999999`; the server formats it into a WhatsApp JID, or
- a full **JID**, e.g. `5511999999999@s.whatsapp.net` (users) or
  `<id>@g.us` (groups).

Number/JID fields are validated by middleware before the handler runs
(`pkg/middleware`), so a bad value returns `400` with an explanatory `error`.
Group, community, and newsletter routes take the corresponding group/community/
newsletter JID instead — see each endpoint's schema in Swagger.

## Optional message fields

Send endpoints share several optional fields on top of the required ones:

| Field | Type | Purpose |
|---|---|---|
| `delay` | int (ms) | Wait before sending (simulate typing). |
| `quoted` | `{ messageId, participant }` | Reply to an existing message. |
| `mentionedJid` | string[] | JIDs to @-mention. |
| `mentionAll` | bool | Mention every group participant. |
| `id` | string | Client-supplied message id. |

## Versioning

Endpoints are mounted at the root (`/instance`, `/send`, …) with **no version
prefix**. Treat the running server's Swagger spec (`/swagger/doc.json`) as the
contract and regenerate your typed client when you upgrade the server.
