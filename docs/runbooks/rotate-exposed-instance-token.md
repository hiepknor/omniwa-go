# Rotate an exposed instance token

Use this runbook when an instance bearer token may have appeared in application
logs, support bundles, shell history, monitoring systems, or another unintended
location. Treat the operation as security-sensitive external state. Production
execution requires the normal L4 approval, backup, and communication gates.

## Before rotation

1. Identify the affected instance by ID without copying the exposed token into
   tickets or chat.
2. Identify every console, integration, and secret store that uses the token.
3. Confirm a recent database backup and a rollback contact.
4. Schedule a short authentication interruption. The current API does not yet
   support overlapping old and new instance tokens.
5. Generate at least 32 random bytes using an approved secret-management tool.
   Do not put the new token on a command line or in an environment variable.

## Rotate

Connect to PostgreSQL over the approved administrative channel and use `psql`
variables so the token is not embedded in the SQL statement or shell history:

```sql
\prompt 'Instance UUID: ' instance_id
\prompt 'New instance token: ' new_token

BEGIN;
SELECT id, name FROM instances WHERE id = :'instance_id'::uuid FOR UPDATE;
UPDATE instances
SET token = :'new_token'
WHERE id = :'instance_id'::uuid
RETURNING id, name;
COMMIT;
```

The update does not change the WhatsApp session or instance ID. Update the
authorized consumer secret stores immediately. Never paste the token into an
issue, deployment annotation, or log verification command.

Reconnect the instance through the authenticated API during the maintenance
window so in-memory credential-keyed caches are refreshed from the updated
record.

## Verify

1. An authenticated request with the new token succeeds.
2. The old token receives HTTP 401.
3. The instance remains connected or can reconnect normally.
4. Application and database logs do not contain either token.
5. Record the rotation time, instance ID, operator, reason, and verification
   result without recording credential material.

## Contain historical copies

- Restrict and expire support bundles that contained the token.
- Apply the log platform's approved deletion or retention process.
- Do not edit immutable audit records in place; restrict access and attach a
  security incident reference when deletion is not permitted.

## Rollback

If consumers cannot be updated, restore a separately generated replacement
token through the same procedure. Do not restore the exposed token. Database
restore is reserved for unrelated data corruption, not credential rollback.
