# Docker

Compose files for running OmniWA GO. All use the published image
`ghcr.io/hiepknor/omniwa-go` (make the GHCR package public, or `docker login
ghcr.io` first). Run the commands **from this `docker/` directory**.

| File | Use case |
|---|---|
| `docker-compose.dev.yml` | **Local development.** Self-contained: app + Postgres + RabbitMQ + MinIO, license gate **off**, values inlined — no `.env` needed. |
| `docker-compose.yml` | **Production base.** App + Postgres only. Reads config from `.env`. |
| `docker-compose.full.yml` | **Production override** adding RabbitMQ + MinIO. Layer on top of the base. |
| `swarm/docker-stack.yml` | **Docker Swarm** deployment reference (Traefik labels, external volumes/network). |

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
curl -s -H 'apikey: dev-global-api-key-change-me' \
  http://localhost:4000/server/capabilities
```

The expected commit, OCI revision label, and `data.revision` response must be
identical before the deployment is accepted.

## Production (base)

```bash
cp .env.example .env      # then edit GLOBAL_API_KEY etc.
docker compose up -d
```

## Production with RabbitMQ + MinIO

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.full.yml up -d
```

## Swarm

Edit `swarm/docker-stack.yml` (domain, secrets, external network/volumes), then:

```bash
docker stack deploy -c swarm/docker-stack.yml omniwa
```

## Notes

- Databases (`omniwa_auth`, `omniwa_users`) are created automatically on startup.
- Set `LICENSE_GATE_ENABLED=false` in `.env` to run without the activation gate.
- Ports: API `4000`, Postgres `5432`, RabbitMQ `5672` (+UI `15672`), MinIO `9000` (+console `9001`).
