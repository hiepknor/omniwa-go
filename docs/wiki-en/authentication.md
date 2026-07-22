# Authentication

OmniWA GO uses a single, simple scheme: an **`apikey` HTTP header** on every
request. There is no login, session, cookie, or JWT flow. There are two tiers of
key.

## The two tiers

| Tier | Header value | Grants access to | Source |
|---|---|---|---|
| **Global admin key** | value of the `GLOBAL_API_KEY` env var | Instance lifecycle: `POST /instance/create`, `GET /instance/all`, `GET /instance/info/{id}`, `DELETE /instance/delete/{id}`, `POST /instance/proxy/{id}`, `POST /instance/forcereconnect/{id}`, `GET /instance/logs/{id}`, and the WebSocket `/ws`. | Set by the server operator. |
| **Instance token** | the `token` returned by `POST /instance/create` | Everything scoped to one WhatsApp account: `/instance/connect`, `/instance/qr`, `/instance/status`, all `/send/*`, `/message/*`, `/chat/*`, `/group/*`, `/user/*`, `/community/*`, `/newsletter/*`, `/label/*`, `/call/*`, `/polls/*`. | Chosen by you in the create request and echoed back. |

The header name is case-insensitive; `apikey` and `ApiKey` both work.

```
apikey: <GLOBAL_API_KEY>          # admin routes
apikey: <instance-token>          # per-instance routes
```

Server implementation: `pkg/middleware/auth_middleware.go` — `AuthAdmin`
compares the header to `GLOBAL_API_KEY`; `Auth` resolves the header to an
instance via its token.

In **Swagger UI**, click **Authorize** and paste the appropriate key; the
documented endpoints (all `/send/*` and the main `/instance/*` routes) will then
send it automatically under *Try it out*.

## Cross-origin WebUI — do NOT put the key in the browser

The apikey is a **bearer secret**: anyone who has it can send messages as that
account. A single-page app that calls the API directly from the browser would
expose the key in its bundle and in every network request the user can inspect.

**Never embed `GLOBAL_API_KEY` or an instance token in front-end code.**

Use a **Backend-for-Frontend (BFF) / proxy** instead:

```
┌──────────────┐   session cookie    ┌───────────────────┐   apikey header   ┌──────────────┐
│  Browser SPA │ ──────────────────► │  Your BFF / proxy │ ────────────────► │  OmniWA API  │
│  (no secret) │ ◄────────────────── │  (holds the key)  │ ◄──────────────── │  :8080       │
└──────────────┘                     └───────────────────┘                   └──────────────┘
```

- The browser authenticates to **your** BFF with its own session (cookie, OAuth, …).
- The BFF holds the OmniWA key server-side and adds the `apikey` header when
  forwarding whitelisted requests.
- For realtime, the BFF also proxies the WebSocket `/ws` (it terminates the
  browser socket and opens an upstream socket carrying the key — see
  [WebSocket Events](websocket-events.md)).

This also lets your BFF enforce per-user authorization, rate limits, and audit
logging that the flat apikey model does not provide.

## CORS note

The server sends permissive CORS headers (`Access-Control-Allow-Origin: *`) and
whitelists the `apikey`/`ApiKey` request headers, so **non-credentialed**
browser requests from any origin are accepted. Do not rely on cookies with
cross-origin requests here: the server also sends
`Access-Control-Allow-Credentials: true`, and the combination of a wildcard
origin with credentials is rejected by browsers per the Fetch spec. The BFF
pattern above sidesteps this entirely — the browser talks only to your own
origin.

## Instance token lookup protection

Instance bearer tokens remain part of the compatibility API during the staged
credential migration. Operators can enable keyed database lookup digests with:

```env
INSTANCE_TOKEN_HMAC_KEY=<base64-encoded secret of at least 32 bytes>
INSTANCE_TOKEN_HMAC_KEY_VERSION=1
INSTANCE_TOKEN_BACKFILL_BATCH=100
INSTANCE_TOKEN_BACKFILL_MAX_BATCHES=10
```

Generate the key with `openssl rand -base64 32`, keep it in a secret manager,
and configure the identical key and version on every replica. Never rotate or
discard that secret ad hoc: the current rollout has a single active digest key
and retains plaintext only as a measured rollback path. Backfill work is
bounded per startup and safely resumes on a later restart.

### Audited token rotation

When an admin-scoped `GET /server/capabilities` response contains
`instance_token_rotation`, rotate an instance token with:

```http
POST /instance/rotate-token/{instanceId}
apikey: <GLOBAL_API_KEY>
Content-Type: application/json
X-Request-ID: <stable 16-64 character request identity>

{
  "expectedVersion": 1,
  "reason": "scheduled operator rotation"
}
```

`expectedVersion` comes from the additive `credentialVersion` field on the
instance create, list, and info views. A successful response returns the new
token and incremented version. Store the token immediately: it is not written
to audit metadata and this endpoint does not reveal it again. The previous
token stops authenticating as soon as the transaction commits.

A `409 credential_version_conflict` means another rotation won; refresh the
instance and deliberately retry with the new version. The endpoint returns
`503 capability_unavailable` when the HMAC key is not configured. Rotation
audit records contain safe metadata only and are not currently a public
history contract.
