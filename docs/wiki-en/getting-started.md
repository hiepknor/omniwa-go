# Getting Started

This walks through the full lifecycle a WebUI drives: create an instance, link a
WhatsApp account by QR, then send a message. Replace `localhost:8080` with your
server and see [Authentication](authentication.md) for where the keys come from.

- **Base URL:** `http://localhost:8080` (the `SERVER_PORT` env var, default `8080`)
- **Content type:** `application/json`
- **Auth:** `apikey` header on every request

> All examples use raw `curl`/`fetch` for clarity. In a real cross-origin WebUI
> these calls go through your BFF/proxy, which injects the `apikey` header — the
> browser never holds it. See [Authentication](authentication.md).

## 1. Create an instance (admin key)

An *instance* is one WhatsApp account the server manages. You pick its `token`;
that token becomes the `apikey` for every later per-instance call.

```bash
curl -X POST http://localhost:8080/instance/create \
  -H "apikey: $GLOBAL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "sales-bot",
    "token": "my-instance-secret-123"
  }'
```

Response (`data` is the full instance record):

```json
{
  "message": "success",
  "data": {
    "id": "b1c2...",
    "name": "sales-bot",
    "token": "my-instance-secret-123",
    "connected": false,
    "jid": "",
    "createdAt": "2026-07-21T10:00:00Z"
  }
}
```

From here on, use `apikey: my-instance-secret-123`.

## 2. Connect and get the QR code

Start the connection, then poll (or subscribe over WebSocket) for the QR image
to display to the user.

```bash
# begin connecting
curl -X POST http://localhost:8080/instance/connect \
  -H "apikey: my-instance-secret-123" \
  -H "Content-Type: application/json" \
  -d '{ "immediate": true }'

# fetch the current QR / pairing payload
curl http://localhost:8080/instance/qr \
  -H "apikey: my-instance-secret-123"
```

QR response:

```json
{
  "message": "success",
  "data": {
    "qrcode": "data:image/png;base64,iVBORw0KGgo...",
    "code": "2@abc123..."
  }
}
```

Render `data.qrcode` as an `<img src>` for the user to scan in WhatsApp →
**Linked devices → Link a device**. The QR rotates; the fresh value is pushed on
the `qrcode` WebSocket event, so the smoothest UX subscribes to `/ws` rather than
polling (see [WebSocket Events](websocket-events.md)).

> **Pairing code alternative:** instead of a QR you can link by phone number via
> `POST /instance/pair` with `{ "phone": "5511999999999" }`, which returns a
> code the user types into WhatsApp.

## 3. Confirm the account is connected

```bash
curl http://localhost:8080/instance/status \
  -H "apikey: my-instance-secret-123"
```

```json
{ "message": "success", "data": { "Connected": true, "LoggedIn": true, "Name": "Sales" } }
```

The `connection` WebSocket event fires on the same transition, so a WebUI can
flip to its "connected" state without polling.

## 4. Send a message

```bash
curl -X POST http://localhost:8080/send/text \
  -H "apikey: my-instance-secret-123" \
  -H "Content-Type: application/json" \
  -d '{
    "number": "5511999999999",
    "text": "Hello from OmniWA GO 👋"
  }'
```

```json
{ "message": "success", "data": { "ID": "3EB0C767D26A8D4E2A1B", "Timestamp": "2026-07-21T10:30:00Z" } }
```

Other `/send/*` endpoints follow the same envelope: `link`, `media`, `location`,
`contact`, `sticker`, `poll`, `button`, `list`, `carousel`, and `status/text` /
`status/media`. See Swagger for each request body.

## 5. Receive messages and events

Incoming messages, delivery/read receipts, presence, and connection changes are
delivered over the WebSocket at `/ws`, **not** by polling REST. Continue to
[WebSocket Events](websocket-events.md).

## Full endpoint reference

For the complete, always-current list of endpoints and request/response schemas,
open **Swagger UI** at `http://localhost:8080/swagger/index.html`. See
[Conventions](conventions.md) for the shared response/error shape.
