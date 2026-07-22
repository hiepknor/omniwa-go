# Non-root container upgrade

OmniWA GO runs as UID and GID `10001` in the container. New named volumes are
initialized with compatible ownership from the image. Existing volumes created
by an older root container may require a one-time ownership migration.

## Pre-deployment check

Resolve the Compose project and inspect both application volumes before stopping
the current service:

```bash
docker compose config --volumes
docker volume inspect <data-volume> <logs-volume>
```

Back up the volumes according to the environment's normal database and file
backup policy. The PostgreSQL databases are separate from `/app/dbdata`; do not
substitute a filesystem copy for a PostgreSQL backup.

## Ownership migration

Stop only the application container so no process writes to these volumes during
the migration. Replace the placeholders with the exact names from the check
above:

```bash
docker compose stop omniwa-go
docker run --rm --user 0:0 -v <data-volume>:/target alpine:3.19 \
  chown -R 10001:10001 /target
docker run --rm --user 0:0 -v <logs-volume>:/target alpine:3.19 \
  chown -R 10001:10001 /target
```

Deploy an immutable image digest and verify its configured user before starting:

```bash
docker image inspect <image-digest> --format '{{.Config.User}}'
```

The expected value is `10001:10001`. Start the service, then verify liveness,
revision metadata, log writes, and any local data writes used by the deployment.

## Rollback

The ownership is compatible with another image that runs as UID `10001`. If an
older emergency image must run as root, it can still read these volumes; no
ownership rollback is required. Roll back the immutable image reference, restart
the application, and record the temporary root-runtime exception for follow-up.
