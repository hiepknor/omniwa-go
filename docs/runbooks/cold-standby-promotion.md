# Cold-standby deployment and promotion

## Scope and safety boundary

This runbook operates the secretless cold standby introduced by ADR 0060. It is
not an automatic failover system. A standby exposes only process health and
cannot acquire database ownership, migrate schemas, connect WhatsApp, or serve
application traffic.

Use exactly one active process per users database. Promotion is stop/recreate;
never change a running standby into active mode and never start an active
candidate before the former owner has stopped and released PostgreSQL
ownership.

## Preconditions

- Record the current and candidate immutable image digests and their source
  revisions.
- Confirm the current active responds 200 on `/server/ready`.
- Configure Caddy or the load balancer to use `/server/ready`, not
  `/server/ok`, for backend selection.
- Confirm database backups and the previous digest satisfy the deployment
  rollback policy.
- Confirm the operations migration service can read only the users and auth
  database DSNs.
- Announce a maintenance window. Cold promotion intentionally interrupts API
  and WhatsApp availability.

## Start and verify the standby

From `docker/`, set `OMNIWA_IMAGE` to the candidate digest and start only the
profile service:

```bash
docker compose --profile standby up -d omniwa-standby
curl --fail http://127.0.0.1:4001/server/live
test "$(curl --silent --output /dev/null --write-out '%{http_code}' \
  http://127.0.0.1:4001/server/ready)" = 503
test "$(curl --silent --output /dev/null --write-out '%{http_code}' \
  http://127.0.0.1:4001/server/capabilities)" = 404
docker inspect omniwa-standby --format '{{json .Mounts}}'
docker inspect omniwa-standby \
  --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'
```

The mounts output must be empty. Inspect `.Config.Env` and reject the standby
if it contains API keys, PostgreSQL, AMQP, NATS, MinIO, Webhook, or license
credentials. Verify the OCI revision against the approved candidate commit.

Do not register loopback port 4001 as a business backend. It is an operator
control-plane endpoint only.

## Controlled promotion

1. Stop new external traffic and wait for in-flight requests to drain within
   the operational timeout.
2. Stop the active application:

   ```bash
   docker compose stop omniwa-go
   ```

3. Verify the old container is stopped. Check PostgreSQL for the ownership
   session if operator tooling provides that evidence. Do not proceed merely
   because one health check timed out.
4. Stop the cold standby; runtime mode is immutable:

   ```bash
   docker compose --profile standby stop omniwa-standby
   ```

5. Run the candidate image's one-shot migrations. Any ownership or migration
   failure aborts promotion:

   ```bash
   docker compose up -d --wait postgres
   docker compose --profile operations run --rm --no-deps omniwa-migrate
   ```

6. Recreate the full application with the candidate digest and secret scope:

   ```bash
   docker compose up -d omniwa-go
   ```

7. Keep traffic disabled until all verification checks pass:

   ```bash
   curl --fail http://127.0.0.1:4000/server/live
   curl --fail http://127.0.0.1:4000/server/ready
   docker inspect omniwa-go \
     --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'
   ```

8. Verify the authenticated capability response, ownership-acquisition log,
   expected migration versions, instance reconnects, durable outbox backlog,
   signed Webhook canary, and error metrics. Restore traffic only after the
   process is ready and the expected instances have recovered.

## Abort and rollback

Before migration succeeds, abort by keeping traffic disabled, restoring the
previous image digest, and starting the previous active application with the
same stop-first sequence.

After an additive migration succeeds, do not roll the schema back. Restore the
previous compatible image digest, leave additive migrations in place, and use
a forward fix for any schema defect. If the previous image is not compatible
with the applied migration, keep traffic disabled and follow the migration ADR
instead of forcing startup.

If ownership acquisition fails, do not bypass the lock. Find and stop the old
owner or repair the PostgreSQL session boundary. If WhatsApp shows competing
connections or duplicate side effects, stop every application candidate,
preserve logs and outbox evidence, and treat the event as a split-brain
incident.

## Failback and cleanup

Failback uses the same controlled promotion procedure; it is not a container
restart. After the observation window, either start a newly verified
secretless standby for the current active digest or remove the stopped standby:

```bash
docker compose --profile standby rm -f omniwa-standby
```

Record recovery duration, ownership evidence, reconnect duration, outbox drain
time, duplicate-delivery observations, and every manual intervention. Do not
approve automatic promotion until fencing and repeated split-brain drills are
implemented in a later phase.
