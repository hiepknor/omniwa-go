# QR Pairing Diagnostics

## Purpose

Use this runbook when a new instance disconnects before the Console receives a
QR code. The Console only reflects backend metadata; QR generation is owned by
the WhatsApp provider connection.

## Safe diagnostics

Set `WADEBUG=DEBUG` for a canary instance or short diagnostic window. The
legacy `DEBUG_ENABLED` variable remains supported, but `WADEBUG` takes
precedence. Return the level to `INFO` after collecting the failure window
because protocol debug logs are verbose.

For an unpaired connection, the application records:

- `pairing_elapsed`: time between starting the provider connection and the
  failure or disconnect event;
- `qr_seen`: whether whatsmeow emitted a QR event during that attempt;
- the typed connect-failure reason and message when WhatsApp sends one.

An `Unpaired client disconnected` record with `qr_seen=false` means the socket
closed before the application received a QR event. Inspect the adjacent
`Client/Socket` records. Do not copy raw credentials, QR contents, tokens, or
cryptographic material into incident reports.

## Rollout

1. Deploy the image to one canary replica with `WADEBUG=INFO`.
2. Confirm existing paired instances reconnect normally.
3. Temporarily set `WADEBUG=DEBUG` on the canary and attempt one new-device
   pairing.
4. Capture the socket lifecycle and the structured pairing observation.
5. Restore `WADEBUG=INFO` before expanding the deployment.

No database, API, event, or credential format changes are involved.

## Rollback

Redeploy the previous immutable image digest. Configuration rollback alone is
also available: keep the new image and set `WADEBUG=INFO`. The added pairing
observation does not control connection behavior and can be removed without a
data migration.
