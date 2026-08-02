# ADR 0055: Operate bounded webhook signing keyrings

- Status: Accepted
- Date: 2026-08-02

## Context

ADR 0054 established deployment-wide HMAC signing but left operational key
selection, environment isolation, receiver deployment, and rotation outside
the application contract. Reusing one secret across staging and production
allows a staging compromise to forge production-origin requests. Replacing a
single receiver secret before changing the sender causes an outage, while
changing the sender first causes valid deliveries to be rejected.

The receiver and its service configuration also need a reviewable source of
truth. Manual server-only code makes rollback and security regression testing
dependent on local state. Egress-IP and clock changes can independently break
the Caddy source restriction or timestamp verification.

## Decision

The operator receiver uses a bounded keyring selected exclusively by the
existing `X-Omniwa-Signature-Key-ID` header. Each safe key ID maps to one
systemd credential name. The receiver accepts at most eight entries, rejects
duplicates, loads only standard-base64 32-byte keys, and fails startup for any
invalid entry. It never tries every secret for an unknown key ID.

Staging and production use independently generated secrets and distinct key
IDs. A sender still has exactly one active key; no application protocol or
durable outbox schema changes are required. Rotation is receiver-first:

```text
receiver accepts old + new
  -> staging signs new and passes an outbox canary
  -> production signs new and passes an outbox canary
  -> overlap window expires
  -> receiver retires old
```

The receiver, tests, hardened systemd units, Caddy example, monitor, and
rotation runbook are versioned under `docker/webhook-receiver`. Receiver
metrics have bounded labels and contain no payload, destination, delivery ID,
or credential. Existing application outbox metrics alert on dead letters,
infrastructure failures, and an aged pending queue. A credential-free systemd
monitor checks enforcement, signature-failure deltas, API health, NTP, and the
declared egress IP.

Exact outbound webhook host allowlists remain mandatory. Caddy retains the
Cloudflare source-CIDR check and exact origin egress addresses as
defense-in-depth; monitoring detects a changed egress address before operators
accept a new value.

## Alternatives

### Give the sender multiple active keys

Sending multiple signatures expands the public protocol and creates ambiguous
receiver behavior. Because outbox payloads are signed at attempt time, one
active sender key plus an overlapping receiver keyring is sufficient.

### Remove the source-IP restriction

HMAC would still authenticate valid requests, but the receiver would absorb
more unauthenticated traffic and lose a useful independent control. Static
egress plus monitoring was selected. mTLS or an authenticated service tunnel
can replace the IP control later.

### Store keys in the application database

That would create encrypted-at-rest storage, tenant authorization, recovery,
and migration requirements unrelated to operator-owned endpoints. File-backed
process credentials keep the present trust boundary and remain outside the
database.

## Consequences

- A staging credential cannot authenticate as production.
- Rotation does not require disabling signature enforcement.
- Compromise of the shared receiver host can still expose every receiver key;
  cryptographic separation is not host isolation.
- Operators must monitor clock synchronization and maintain exact egress and
  destination allowlists.
- Receiver deployment becomes repeatable, testable, and independently
  rollbackable without rebuilding the OmniWA application image.

## Rollout and rollback

Install the versioned receiver in backward-compatible single-key mode, then
load the legacy, staging, and production credentials together. Verify all
three signatures and rejection cases before changing a sender. Change staging,
run a durable outbox canary, observe, and then repeat for production. Retire the
legacy key only after the overlap window and an explicit old-key rejection
test.

Rollback restores receiver acceptance of the prior key first, then restores
the affected sender's previous secret file and key ID. Each service and Caddy
change retains a timestamped configuration backup. Durable outbox rows are
never deleted or rewritten during key rotation.
