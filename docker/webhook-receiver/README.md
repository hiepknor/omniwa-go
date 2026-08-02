# Operator Webhook Receiver

This directory versions the minimal receiver and operational controls used for
an operator-owned OmniWA webhook endpoint. It is not a general multi-tenant
webhook service.

## Security contract

The receiver verifies `X-Omniwa-Signature` over the exact raw request bytes
before JSON parsing or delivery-state mutation. A five-minute timestamp window
limits replay, while the stable delivery UUID is retained as a SHA-256 digest
for seven days so confirmed retries are idempotent `2xx` no-ops.
The loopback HTTP server limits active requests to 32, queues at most 64
connections, caps request bodies at 4 MiB, and applies a 10-second socket
timeout as defense-in-depth behind the reverse proxy.

`OMNIWA_WEBHOOK_SIGNATURE_KEYS` is a comma-separated, bounded mapping from a
public key ID to a systemd credential name:

```text
staging-2026-08=webhook_staging,production-2026-08=webhook_production
```

At most eight exact key IDs are accepted. Credential names cannot contain path
separators, every standard-base64 credential must decode to exactly 32 bytes,
and duplicate key IDs or credential names fail startup. The legacy single-key
environment remains supported only to stage migration to the keyring.

## Install

Generate each secret independently and keep it outside the repository:

```bash
umask 077
openssl rand -base64 32 > /etc/omniwa-webhook/staging-secret
openssl rand -base64 32 > /etc/omniwa-webhook/production-secret
```

Run `sudo ./install.sh`, install a private drop-in based on
`signature-keyring.conf.example`, then validate and start the receiver:

```bash
sudo systemctl daemon-reload
sudo systemctl restart omniwa-webhook-receiver
curl -fsS http://127.0.0.1:9000/healthz
curl -fsS http://127.0.0.1:9000/stats
```

The Caddy example requires both a Cloudflare source address and one of the
declared `CF-Connecting-IP` values. Replace every example hostname and address,
run `caddy validate`, and retain the previous config until the signed canary
passes.

## Zero-downtime rotation

1. Generate a new 32-byte secret and a new key ID.
2. Add its credential and mapping to the receiver while retaining the old key.
3. Restart the receiver and send valid, tampered, stale, and duplicate probes.
4. Change only the staging sender secret file and key ID; recreate it and run a
   durable outbox canary.
5. Repeat for production after staging observation.
6. Retain the old accepted key for at least the replay window plus the maximum
   in-flight request duration. Outbox retries are signed when attempted and use
   the sender's current active key.
7. Remove the old key from the receiver, restart, and confirm an old-key probe
   receives `401` while both active-key probes succeed.
8. Delete obsolete secret copies and any backups containing them.

Rollback adds the old receiver key back first, then restores the sender's old
key ID and secret mount. Never disable receiver enforcement as part of normal
rotation, and never delete durable outbox rows.

## Monitoring

The receiver exposes aggregate-only `/metrics` and `/stats` endpoints on its
loopback listener. Metrics include bounded signature outcomes, configured key
IDs, enforcement mode, and duplicate deliveries; they never expose payloads,
delivery IDs, destinations, or credentials.

The oneshot monitor checks receiver/API health, enforcement mode, signature
failure counter deltas, NTP synchronization, and the expected public egress IP.
It prefers `timedatectl` and falls back to Chrony's `Leap status: Normal` when a
systemd dynamic user cannot access timedated over D-Bus.
It can also authenticate to each application's loopback `/metrics` endpoint by
systemd credential and alerts when the dead-letter gauge increases or the
oldest pending delivery exceeds five minutes. The credential is bounded to
4 KiB, used only as the `apikey` request header, and never logged. Authenticated
metrics targets must be exact HTTP loopback `/metrics` URLs, and redirects are
disabled so an API key cannot be forwarded to another destination.
Install a private drop-in based on `monitor.conf.example`, run the service once
to establish its baseline, and then enable the timer:

```bash
sudo systemctl start omniwa-webhook-monitor.service
sudo systemctl enable --now omniwa-webhook-monitor.timer
```

A non-zero monitor exit is an alert signal. Connect systemd failures and the
Prometheus rules in `../prometheus/alerts.yml` to the operator's notification
channel. Use a signed outbox canary after deployments and rotations; the
monitor deliberately has no signing credential and cannot forge deliveries.

## Validation

```bash
python3 -m unittest -v test_receiver.py test_monitor.py
sh -n install.sh
```
