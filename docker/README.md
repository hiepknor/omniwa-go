# Docker

Compose files for running OmniWA GO. Deployment files use the published image
`ghcr.io/hiepknor/omniwa-go` (make the GHCR package public, or `docker login
ghcr.io` first); the CI smoke stack builds the local Dockerfile. Run operational
commands **from this `docker/` directory**.

| File | Use case |
|---|---|
| `docker-compose.dev.yml` | **Local development.** Self-contained: app + Postgres + RabbitMQ + MinIO + Prometheus, license gate **off**, values inlined — no `.env` needed. |
| `docker-compose.yml` | **Production base.** App + Postgres only. Reads config from `.env`. |
| `docker-compose.full.yml` | **Production override** adding RabbitMQ + MinIO. Layer on top of the base. |
| `docker-compose.smoke.yml` | **CI only.** Builds and verifies the production Dockerfile against isolated Postgres. |
| `swarm/docker-stack.yml` | **Docker Swarm** deployment reference (Traefik labels, external volumes/network). |
| `webhook-receiver/` | Versioned operator-owned HMAC receiver, rotation, Caddy, and monitoring assets. |

## Development

```bash
export OMNIWA_IMAGE=ghcr.io/hiepknor/omniwa-go:sha-<40-character-main-commit>
docker compose -f docker-compose.dev.yml pull omniwa-go
docker compose -f docker-compose.dev.yml up -d
curl http://localhost:4000/server/ok
```

`OMNIWA_IMAGE` is required and must identify the intended build with an
immutable `sha-<40-character-commit>` tag or, preferably, a digest such as
`ghcr.io/hiepknor/omniwa-go@sha256:...`. The development stack intentionally
has no `latest` fallback.

Verify the running source revision against the container label and API metadata:

```bash
docker inspect omniwa-go --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}'
curl -s -H "apikey: $GLOBAL_API_KEY" \
  http://localhost:4000/server/capabilities
```

The expected commit, OCI revision label, and `data.revision` response must be
identical before the deployment is accepted.

### Durable external events

Webhook and RabbitMQ events are atomically recorded with durable history and
are always delivered by the PostgreSQL outbox worker. NATS and WebSocket remain
direct realtime transports. Before rolling back to an image that still has
direct adapters, drain pending and processing outbox rows, then configure that
image with serving and both durable emit transports enabled. See ADR 0048.

Production Webhooks remain fail-closed until their exact hostname is present in
`WEBHOOK_ALLOWED_HOSTS`. Use port `443`, keep `WEBHOOK_ALLOW_PRIVATE=false`, and
enable HMAC signatures for an operator-owned receiver. Follow the
[Webhook outbound security, signature, and phone-number rollout runbook](../docs/wiki-en/webhook-outbound-security.md)
before enabling `SEND_MESSAGE` for an instance.

Use separate signing credentials and key IDs for staging and production. The
operator receiver supports a bounded keyring so a new key can be accepted
before either sender changes. Follow the
[receiver rotation runbook](webhook-receiver/README.md); do not share one
secret across environments or remove an old receiver key before the staged
outbox canary succeeds.

### Development metrics

The development stack persists Prometheus data in the `prometheus_data` named
volume for 30 days, bounded to 1 GB. Its UI and API listen only on
`http://127.0.0.1:9090`; the scraper reaches the authenticated application
`/metrics` endpoint over the internal Compose network. Both services receive
the same `GLOBAL_API_KEY` value. Prometheus writes that value to a private
runtime file and uses it as the custom `apikey` request header, so the scrape
configuration contains no credential.

Check scrape health and migration traffic:

```bash
curl -fsS --get http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=up{job="omniwa-go"}'
curl -fsS --get http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=sum(increase(omniwa_conversation_api_requests_total{contract="conversation",status=~"4xx|5xx"}[24h])) or vector(0)'
```

Changing `GLOBAL_API_KEY` requires recreating both `omniwa-go` and
`prometheus`. Removing the Prometheus service leaves application behavior and
data unchanged. Delete `prometheus_data` only when its monitoring history is
intentionally no longer needed.

Images run as the non-root user `10001:10001`. Before upgrading an existing
installation that has root-owned application volumes, follow the
[non-root container upgrade runbook](../docs/runbooks/non-root-container-upgrade.md).

## Production (base)

```bash
cp .env.example .env      # set immutable image digests and secret file paths
docker compose up -d
```

Production Compose has no mutable tag fallback. `OMNIWA_IMAGE` must use the
verified `ghcr.io/hiepknor/omniwa-go@sha256:...` value recorded by the publish
workflow, and `POSTGRES_IMAGE` must also identify an immutable digest. Keep the
previous digests as rollback targets.

### One-shot database migration

The image exposes a `migrate` command that upgrades the users database, the
WhatsApp auth store, and the licensed runtime table when the license gate is
enabled. The command takes the same single-replica PostgreSQL ownership lock as
the server and fails while an application process still owns the database.

For a controlled production migration, stop the application, run the
least-privilege operations service, and start the application again:

```bash
docker compose stop omniwa-go
docker compose up -d --wait postgres
docker compose --profile operations run --rm --no-deps omniwa-migrate
docker compose up -d omniwa-go
curl --fail http://localhost:4000/server/ready
```

The operations service receives only the users and auth database DSNs. Running
the command repeatedly is supported. A failed command must not be bypassed by
starting the new application image; restore the previous image digest or fix
the database failure and rerun the same command.

### Secretless cold standby

The optional standby profile runs the same immutable image with only
`RUNTIME_MODE=standby` and `SERVER_PORT`. It mounts no env file, secret, or
volume; binds its control plane to `127.0.0.1:4001`; stays live; and always
fails readiness. It does not connect to PostgreSQL or WhatsApp and cannot serve
application routes.

```bash
docker compose --profile standby up -d omniwa-standby
curl --fail http://127.0.0.1:4001/server/live
test "$(curl -s -o /dev/null -w '%{http_code}' \
  http://127.0.0.1:4001/server/ready)" = 503
```

Do not put `/server/ok` behind business routing: that compatibility endpoint is
200 on both active and standby processes. Caddy or the orchestrator must select
traffic owners with `/server/ready`. Promotion is a controlled stop, migrate,
and recreate operation; there is no in-process or automatic promotion. Follow
the [cold-standby promotion runbook](../docs/runbooks/cold-standby-promotion.md).

The production stack does not accept built-in credentials. Before rendering it,
materialize the global API key, PostgreSQL password, and both application DSNs
at the paths configured by `OMNIWA_*_FILE`. Files under `docker/secrets/` are
ignored by Git, but the operator must still restrict their host permissions.
See the [production secrets runbook](../docs/runbooks/production-secrets.md) for
initial migration, verification, rotation, and rollback.

## Production with RabbitMQ + MinIO

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.full.yml up -d
```

The full override additionally requires immutable `RABBITMQ_IMAGE` and
`MINIO_IMAGE` values plus the AMQP URL, RabbitMQ configuration, and MinIO root
secret files listed in `.env.example`. RabbitMQ and MinIO management ports bind
to loopback; PostgreSQL is available only on the private Compose network.

## Swarm

Create the three external secrets referenced by `swarm/docker-stack.yml`, edit
the domain and external network/volumes, set the verified image digest, then:

```bash
export OMNIWA_IMAGE=ghcr.io/hiepknor/omniwa-go@sha256:<verified-digest>
docker stack deploy -c swarm/docker-stack.yml omniwa
```

## Image publication and release promotion

An immutable `sha-<40-character-commit>` image is built only after the CI run
for that exact `main` commit succeeds. The build publishes a multi-platform
manifest, SBOM, provenance, and digest; rerunning the workflow reuses and
verifies an existing SHA image instead of overwriting it.

Publishing a GitHub release promotes the existing SHA digest to the exact
semantic Git tag without rebuilding. Promotion verifies the Git tag, `VERSION`,
OCI revision/version labels, runtime user, and digest. It fails if the release
alias already points elsewhere. Maintained deployment files never consume a
release alias or `latest`; they require the recorded digest.

## Deployment topology

Run exactly one application replica per users database. The process enforces
this with a PostgreSQL advisory lock and intentionally rejects a second replica.
Use stop-first/Recreate upgrades, not start-first or surge rollouts. See the
[single-replica deployment runbook](../docs/runbooks/single-replica-deployment.md).

## Notes

- Databases (`omniwa_auth`, `omniwa_users`) are created automatically on startup.
- Set `LICENSE_GATE_ENABLED=false` in `.env` to run without the activation gate.
- Set `HTTP_ALLOWED_ORIGINS` to a comma-separated list of exact `http://` or
  `https://` browser origins. The same policy protects HTTP and WebSocket
  handshakes; same-host requests and clients without an `Origin` header remain
  allowed. Wildcards, URL paths, credentials, queries, and fragments are
  rejected during startup.
- Keep `HTTP_TRUSTED_PROXIES` empty when the API is directly exposed. When a
  private reverse proxy such as Caddy is used, set it to only the proxy IPs or
  CIDRs (for same-host native Caddy, normally `127.0.0.1,::1`) and prevent
  direct public access to the backend port. Authentication failure throttling
  uses this trusted client-IP boundary.
- Sensitive application settings support `NAME_FILE` as an additive alternative
  to `NAME`. Configuring both with non-empty values fails startup.
- Ports: API `4000`; development Prometheus `127.0.0.1:9090`; production full
  RabbitMQ `127.0.0.1:5672` (+UI `127.0.0.1:15672`) and MinIO
  `127.0.0.1:9000` (+console `127.0.0.1:9001`). Production PostgreSQL is not
  host-published.
