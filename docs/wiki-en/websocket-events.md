# WebSocket Events (realtime)

Incoming messages, connection changes, QR refreshes, receipts, presence and more
are delivered in realtime over a single WebSocket. This is the channel a WebUI
uses to stay live — REST is only for actions you initiate.

> OpenAPI 2.0 cannot describe WebSockets, so `/ws` does **not** appear in Swagger.
> This page is its contract. Source: `cmd/evolution-go/main.go` (the `/ws` route)
> and `pkg/events/websocket/websocket_producer.go`.

## Endpoint

```
ws://localhost:8080/ws            # all instances (broadcast)
ws://localhost:8080/ws?instanceId=<instanceId>   # one instance only
```

- Omit `instanceId` → you receive events for **every** instance (broadcast).
- Pass `instanceId` → you receive events for that instance only.
- Use `wss://` when the server is behind TLS.

## Authentication — the global key over a subprotocol

Browsers cannot set custom headers on a WebSocket handshake, so the token travels
as the **second value of the `Sec-WebSocket-Protocol` header**, which the browser
*can* set via the second argument of the `WebSocket` constructor:

```js
const ws = new WebSocket(
  "ws://localhost:8080/ws?instanceId=sales-bot",
  ["apikey", GLOBAL_API_KEY]   // ["apikey", "<token>"]
);
```

The server reads the second protocol value and compares it to `GLOBAL_API_KEY`.

> **Important:** `/ws` authenticates with the **global admin key**, not the
> per-instance token — even when you filter by `instanceId`. Because that key is
> a secret, a browser must **not** open this socket directly. Have your
> **BFF/proxy** terminate the browser's WebSocket and open the upstream `/ws`
> with the key attached (see [Authentication](authentication.md)). A failed auth
> closes the socket with `401` and body `{ "error": "..." }`.

## Message frame

Every message the server pushes is a JSON object with two fields:

```json
{ "queue": "message", "payload": "{...}" }
```

| Field | Type | Notes |
|---|---|---|
| `queue` | string | The event type, **lowercased** (e.g. `MESSAGE` → `"message"`). Use it to route. |
| `payload` | string | A **JSON-encoded string** — call `JSON.parse(payload)` to get the event object. Its shape depends on the event type (it mirrors the corresponding webhook payload). |

Minimal client:

```js
ws.onmessage = (evt) => {
  const { queue, payload } = JSON.parse(evt.data);
  const data = JSON.parse(payload);      // payload is itself a JSON string
  switch (queue) {
    case "qrcode":     showQr(data); break;
    case "connection": updateConnState(data); break;
    case "message":    appendIncoming(data); break;
    // ...
  }
};
```

## Event catalog

The event types are defined in `pkg/internal/event_types/event_types.go`. The
`queue` field on the wire is the lowercased form of each name.

| Event (`queue`) | Fires when |
|---|---|
| `message` | An inbound message is received. |
| `send_message` | A message is sent through the API (echo of your own sends). |
| `read_receipt` | A delivery/read receipt arrives. |
| `presence` | A contact's online/last-seen presence changes. |
| `chat_presence` | A "typing…"/"recording…" indicator in a chat. |
| `history_sync` | A batch of history is synced (e.g. right after linking). |
| `call` | An incoming call is offered/updated. |
| `connection` | The instance's connection/login state changes (connected, logged out, …). |
| `qrcode` | A new QR / pairing payload is available to display. |
| `contact` | A contact record is added/updated. |
| `group` | Group metadata or membership changes. |
| `newsletter` | A newsletter/channel update. |
| `label` | A label is created/edited/applied. |
| `button_click` | A user taps an interactive button/list item. |
| `picture` | A profile/group picture changes. |
| `user_about` | A contact's "about"/status text changes. |

> `ALL` also exists in the source as a **subscription selector** (subscribe to
> every event) used when creating/connecting an instance via the `subscribe`
> field — it is not emitted as a `queue` value on the wire.

## Choosing what an instance emits

Which events an instance produces is controlled by its subscription list, set at
`POST /instance/connect` (the `subscribe` array) or when creating the instance.
Pass `["ALL"]` to receive everything, or a subset such as
`["MESSAGE","CONNECTION","QRCODE"]`. The connect response echoes the effective
list as `eventString`.

## Reconnection

The server does not buffer missed events for a disconnected socket. A WebUI (or
its BFF) should:

1. Reconnect with backoff when the socket closes.
2. On reconnect, re-sync current state via REST (`GET /instance/status`, and your
   own message store) rather than assuming continuity.

## Other transports

WebSocket is one of several event producers; the same events can also be
delivered by **webhook**, **RabbitMQ**, or **NATS** (configured per instance via
`webhookUrl` / `rabbitmqEnable` / `natsEnable` / `websocketEnable`). For a WebUI,
the WebSocket is usually the right choice; a webhook to your BFF is a good
alternative when you want server-side persistence of every event.
