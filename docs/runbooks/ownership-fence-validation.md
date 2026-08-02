# Ownership fence validation

This runbook validates the PostgreSQL ownership-epoch and side-effect-fence
contract before any provider-connected split-brain rehearsal. It is a bounded
L3 failure drill. It does not send WhatsApp commands, start an application
replica, or authorize automatic promotion.

## Safety boundary

Run this drill only against a disposable, isolated PostgreSQL database. Never
point `TEST_POSTGRES_DSN` at staging or production: the drill acquires the
application ownership advisory lock, advances the durable epoch repeatedly,
and deliberately terminates one database backend.

Use a clean checkout of the exact candidate revision. Do not run another
OmniWA process or test suite against the drill database. The database must be
reachable only from the operator workstation or CI job and must contain no
customer data.

## What the drill proves

The PostgreSQL integration tests exercise these invariants:

1. An old callback admitted under epoch N holds the shared side-effect fence.
2. Activation of epoch N+1 waits until that admitted callback drains.
3. After activation, an epoch-N command returns `ErrEpochStale` without
   invoking its callback.
4. A command using epoch N+1 is admitted normally.
5. Losing the transaction connection after a callback ran returns
   `ErrSideEffectOutcomeUnknown`, preventing a blind retry.
6. Eight concurrent commands through a two-connection pool wait in a bounded
   queue and drain without leaking an in-use connection.

The transition scenario runs 25 consecutive times per test invocation. The
recommended evidence command repeats the complete set three times under the
race detector, for 75 consecutive ownership transitions.

## Execute on a disposable database

The following example creates and removes its own PostgreSQL 16 container. Use
a unique container name and host port if the defaults conflict with another
local service.

```bash
docker run --rm --detach \
  --name omniwa-ownership-fence-drill \
  --env POSTGRES_PASSWORD=omniwa_drill \
  --env POSTGRES_DB=omniwa_drill \
  --publish 127.0.0.1:55432:5432 \
  postgres:16-alpine

until docker exec omniwa-ownership-fence-drill \
  pg_isready --username postgres --dbname omniwa_drill; do
  sleep 1
done

TEST_POSTGRES_DSN='postgres://postgres:omniwa_drill@127.0.0.1:55432/omniwa_drill?sslmode=disable' \
  go test -race -count=3 -v \
  -run 'TestPostgres(OwnershipFenceRepeatedSplitBrainDrill|SideEffectFenceReportsUnknownOutcomeAfterConnectionLoss|SideEffectFenceDrainsBoundedPoolSaturation)$' \
  ./pkg/instance/ownership

docker stop omniwa-ownership-fence-drill
```

Stop and remove the container even when the test fails. Treat an unexpected
process sharing the drill database as invalid evidence and rerun from a new
database.

## Evidence record

Retain the following in the restricted engineering evidence store; do not
commit generated drill output:

- exact 40-character repository revision and UTC start/end timestamps;
- PostgreSQL image digest and `SELECT version()` output;
- the complete test command and unedited exit status;
- race-detector result, consecutive-transition count, maximum callback-drain
  duration, bounded-pool waiter count, and final in-use connection count;
- any failure output and confirmation that the disposable database was
  removed.

Evidence is acceptable only when every test invocation passes, the race
detector is clean, all 75 transitions complete, the unknown-outcome test sees
the typed error, and pool saturation drains to zero in-use connections.

## What remains unproven

This database drill cannot prove WhatsApp socket teardown, provider reconnect,
duplicate inbound-event behavior, webhook receiver deduplication, traffic
drain timing, or a safe end-to-end RTO. It also cannot determine the remote
outcome of a provider mutation whose connection fails after transmission.

Automatic promotion therefore remains disabled after this drill passes. The
next gate is an explicitly authorized staging rehearsal using a consented
provider canary, versioned traffic-drain and post-promotion probes, receiver
delivery-ID deduplication checks, and the controlled failover evidence runner.
A later ADR must review repeated staging evidence before changing promotion
policy.

## Failure and rollback

There is no application or schema rollback for this validation-only change.
Delete the disposable database and fix the implementation or test harness
before rerunning. Never weaken an assertion or reduce the repetition count to
make an unstable result pass.

If this drill is accidentally pointed at a shared environment, stop it
immediately, keep traffic closed, and follow the cold-standby recovery path.
Advancing the durable epoch may fence the running process; do not decrement or
delete the epoch row.
