# Production secret deployment runbook

Use this runbook to migrate Docker Compose or Swarm deployments from direct
environment credentials to file-backed application secrets. This procedure
does not rotate provider credentials; the first migration must materialize the
values that the running services already use.

## Required material

Base Compose requires four restricted files:

- global API key
- PostgreSQL superuser password
- complete PostgreSQL authentication-database DSN
- complete PostgreSQL users-database DSN

The two DSNs must contain the same URL-encoded password as the PostgreSQL
password file. The full Compose override additionally requires:

- AMQP URL
- a RabbitMQ configuration file containing `default_user`, `default_pass`, and
  `default_vhost`
- MinIO root user
- MinIO root password

Materialize these files from the deployment secret manager. Do not commit them,
paste their values into tickets, or print them in shell output. Restrict the
secret directory to the deployment operator and each file to owner read/write.
The repository ignores `docker/secrets/`, but ignore rules are not an access
control.

Set the corresponding `OMNIWA_*_FILE` paths and immutable image digests in
`docker/.env`. Validate before changing the running service:

```bash
docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml -f docker-compose.full.yml config --quiet
```

Generated Compose output contains secret mount metadata and paths, not secret
contents. Treat it as operational metadata nonetheless.

## Compose migration

1. Back up the users and authentication databases and record the currently
   deployed application and dependency image digests.
2. Deploy the binary that supports `NAME_FILE` while retaining existing direct
   variables. Verify liveness and an authenticated request.
3. Materialize secret files with the existing values and validate both Compose
   renderings.
4. Remove the corresponding non-empty direct values from `docker/.env`. The
   application intentionally rejects simultaneous direct and file sources.
5. Recreate the stack with stop-first semantics.
6. Verify liveness, capabilities, database-backed Conversation reads, and every
   enabled RabbitMQ or MinIO operation. Restart the application once and repeat
   the checks.
7. Inspect the application container configuration and confirm that direct
   sensitive variables are absent and only `*_FILE` paths remain.

Changing `POSTGRES_PASSWORD_FILE` does not rotate a password in an existing
PostgreSQL data volume. Likewise, replacing RabbitMQ or MinIO bootstrap secrets
does not guarantee an existing persisted account changes. Use each provider's
authenticated rotation procedure before replacing those files and DSNs.

## Swarm migration

Create external Swarm secrets for the global API key and both PostgreSQL DSNs.
Their default names are:

- `omniwa_global_api_key`
- `omniwa_postgres_auth_dsn`
- `omniwa_postgres_users_dsn`

Custom names can be supplied through the matching
`OMNIWA_*_SECRET_NAME` interpolation variables. Swarm secret updates are
immutable: create a versioned replacement, update the stack reference, deploy
stop-first, verify, and remove the old secret only after rollback is no longer
needed.

## Rollback

If application file loading fails, remove the affected `NAME_FILE` setting,
restore its direct environment value, and recreate only the application
service. Do not configure both sources. Reverting the application secret source
does not require a database migration or provider credential change.

If provider connectivity fails after an intentional credential rotation, roll
back the provider credential and matching DSN or integration URL as one unit.
Never roll back to the predictable credentials previously shown in repository
examples.
