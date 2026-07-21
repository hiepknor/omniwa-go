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
docker compose -f docker-compose.dev.yml up -d
curl http://localhost:4000/health
```

Pin a version: `IMAGE_TAG=0.7.2 docker compose -f docker-compose.dev.yml up -d`.

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
