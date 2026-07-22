# Docker

Compose files for running OmniWA GO. Deployment files use the published image
`ghcr.io/hiepknor/omniwa-go` (make the GHCR package public, or `docker login
ghcr.io` first); the CI smoke stack builds the local Dockerfile. Run operational
commands **from this `docker/` directory**.

| File | Use case |
|---|---|
| `docker-compose.dev.yml` | **Local development.** Self-contained: app + Postgres + RabbitMQ + MinIO, license gate **off**, values inlined — no `.env` needed. |
| `docker-compose.yml` | **Production base.** App + Postgres only. Reads config from `.env`. |
| `docker-compose.full.yml` | **Production override** adding RabbitMQ + MinIO. Layer on top of the base. |
| `docker-compose.smoke.yml` | **CI only.** Builds and verifies the production Dockerfile against isolated Postgres. |
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
curl -s -H "apikey: $GLOBAL_API_KEY" \
  http://localhost:4000/server/capabilities
```

The expected commit, OCI revision label, and `data.revision` response must be
identical before the deployment is accepted.

Images run as the non-root user `10001:10001`. Before upgrading an existing
installation that has root-owned application volumes, follow the
[non-root container upgrade runbook](../docs/runbooks/non-root-container-upgrade.md).

## Production (base)

```bash
cp .env.example .env      # then set OMNIWA_IMAGE to a digest and edit secrets
docker compose up -d
```

Production Compose has no mutable tag fallback. `OMNIWA_IMAGE` must use the
verified `ghcr.io/hiepknor/omniwa-go@sha256:...` value recorded by the publish
workflow. Keep the previous digest as the rollback target.

## Production with RabbitMQ + MinIO

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.full.yml up -d
```

## Swarm

Edit `swarm/docker-stack.yml` (domain, secrets, external network/volumes), set
the verified image digest, then:

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
- Ports: API `4000`, Postgres `5432`, RabbitMQ `5672` (+UI `15672`), MinIO `9000` (+console `9001`).
