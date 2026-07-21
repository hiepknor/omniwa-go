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

## Errors

Failures return an HTTP 4xx/5xx status with a single-field body:

```json
{ "error": "phone number is required" }
```

| Status | Meaning |
|---|---|
| `400 Bad Request` | Validation failed — missing/invalid field, or a malformed phone/JID. The `error` string says which. |
| `401 Unauthorized` | Missing or wrong `apikey` (or, on `/ws`, a bad token). |
| `404 Not Found` | The instance or resource does not exist. |
| `500 Internal Server Error` | Unexpected server/WhatsApp error; `error` carries the detail. |

Always branch on the HTTP status first, then surface `error` to the user.

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
