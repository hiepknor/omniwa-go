# Production preflight

Run this gate immediately before directing production traffic to a new OmniWA
GO image. It is read-only and fails closed when the active revision, runtime
health, outbox state, or an operator-supplied media probe is unsafe.

## Preconditions

- CI and image publication succeeded for the exact `main` commit.
- The deployment uses the published immutable digest, not a mutable tag.
- PostgreSQL has a current backup and the prior application digest is recorded.
- The load balancer uses `/server/ready`, never `/server/ok`, for business
  traffic ownership.
- RabbitMQ and Webhook consumers deduplicate the stable delivery ID. The sample
  receiver does this already; every independent RabbitMQ consumer must provide
  the same guarantee using the AMQP `MessageId`.
- Prometheus has loaded `docker/prometheus/alerts.yml` and the alert delivery
  path has been tested.

Store the global API key in a private file rather than an environment variable:

```bash
install -m 0600 /dev/null /run/omniwa-preflight-api-key
# Write the key using the approved secret-management mechanism.
```

## Optional media probe

When media is production-critical, provide an absolute executable path through
`OMNIWA_PREFLIGHT_MEDIA_PROBE`. The operator owns this probe because the
application readiness contract does not currently include MinIO. It should use
least-privilege credentials to write, read, compare, and delete a randomized
small object in a dedicated synthetic prefix in every enabled private bucket.
It must not print credentials, endpoints, bucket names, object keys, or object
contents. The runner rejects a group- or world-writable probe.

Do not use MinIO's unauthenticated liveness endpoint as a substitute: it does
not prove that the application credential can access the configured bucket.

## Run the gate

```bash
export OMNIWA_PREFLIGHT_API_URL=https://api.example.com
export OMNIWA_PREFLIGHT_API_KEY_FILE=/run/omniwa-preflight-api-key
export OMNIWA_PREFLIGHT_EXPECTED_REVISION=<40-character-main-commit>
export OMNIWA_PREFLIGHT_MEDIA_PROBE=/usr/local/libexec/omniwa-media-probe

bash scripts/ops/production-preflight.sh
```

The defaults reject any existing external-event dead letter and an oldest
pending delivery older than 300 seconds. An explicitly approved maintenance
window may set `OMNIWA_PREFLIGHT_MAX_DEAD_LETTERS` or
`OMNIWA_PREFLIGHT_MAX_PENDING_AGE_SECONDS` to a reviewed non-negative value.
Record the reason and the original values; never increase a threshold merely
to make the gate pass.

The command also verifies authenticated liveness, active readiness, exact
runtime revision, and all required outbox metrics. It does not write a report
containing endpoints or credentials. Capture only its bounded final line in
the private deployment record.

## Failure response

- Revision mismatch: stop the rollout and resolve the image/digest selection.
- Readiness failure: keep traffic on the previous revision and investigate
  process role, ownership, and PostgreSQL.
- Old pending delivery: restore the transport and allow the durable worker to
  drain before retrying.
- Dead letter: investigate its bounded error code and follow the approved
  recovery procedure. Do not delete or rewrite outbox rows to pass the gate.
- Media probe failure: keep media-dependent capabilities disabled or roll back;
  validate TLS, credentials, bucket isolation, quota, and object lifecycle.

## Post-deployment verification

Run the gate again after traffic is enabled, then observe for at least one full
alert evaluation window. Send one authorized canary event and verify that the
outbox returns to zero without a new dead letter. For media-enabled deployments,
run the same synthetic media probe again.

Rollback uses the recorded prior digest. Do not roll back to an image with
direct event adapters until pending and processing outbox rows have drained.
