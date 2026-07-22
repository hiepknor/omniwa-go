# OmniWA GO — WebUI Integration Guide

English documentation for developers building a **client WebUI** on top of the
OmniWA GO HTTP API (a WhatsApp API built on [whatsmeow](https://github.com/tulir/whatsmeow)).

> This folder is the fork's English documentation. The Portuguese guides under
> [`docs/wiki/`](../wiki/) are inherited from upstream and are **not** kept in
> sync with this fork — prefer the pages here and the live Swagger reference.

## Reference vs. guides

| Source | What it is | When to use |
|---|---|---|
| **Swagger UI** — `http://localhost:8080/swagger/index.html` | Auto-generated OpenAPI reference, always in sync with the code. Every request/response schema, plus an **Authorize** button. | The authoritative endpoint reference. Generate a typed client from `/swagger/doc.json`. |
| **These guides** | Hand-written narrative: auth model, end-to-end flows, and the realtime WebSocket stream (which OpenAPI 2.0 cannot describe). | Read first to understand how the pieces fit together. |

## Contents

1. **[Getting Started](getting-started.md)** — base URL, the create → connect →
   scan QR → send flow, first API call.
2. **[Authentication](authentication.md)** — the two-tier `apikey` scheme and how
   a **cross-origin WebUI** must proxy it (never ship the key in the browser).
3. **[Conventions](conventions.md)** — response envelope, error format, phone/JID
   formatting rules.
4. **[WebSocket Events](websocket-events.md)** — the realtime `/ws` stream: the
   handshake, the message frame, and the full catalog of event types.
5. **[Message Retention](message-retention.md)** — projected message storage,
   deletion timing, privacy responsibilities, and operational behavior.
6. **[Campaign Orchestration](campaigns.md)** — consent-backed durable sends,
   lifecycle controls, pagination, audit history, and delivery guarantees.

## The API in one paragraph

The server exposes REST endpoints grouped by resource (`/instance`, `/send`,
`/message`, `/chat`, `/group`, `/community`, `/newsletter`, `/label`, `/user`,
`/call`, `/polls`, `/campaigns`). Every call carries an `apikey` HTTP header. You
first create an **instance** (one connected WhatsApp account) using the global admin key, then
use that instance's own token as the `apikey` for all messaging routes. Realtime
updates (incoming messages, connection state, QR refresh, …) arrive over a
single WebSocket at `/ws`.
