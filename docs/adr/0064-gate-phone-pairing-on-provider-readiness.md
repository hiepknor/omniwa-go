# ADR 0064: Gate phone pairing on provider readiness

- Status: Accepted
- Date: 2026-08-03

## Context

Phone-code pairing started a WhatsApp runtime asynchronously, slept for three
seconds, and then called `PairPhone`. Whatsmeow requires callers to wait for its
first QR event before requesting a phone pairing code. Runtime construction,
web-version discovery, websocket setup, and QR delivery have no fixed latency,
so the sleep could expire either before the runtime existed or before the
provider was ready.

An unpaired websocket disconnect also entered the normal automatic reconnect
path. Because an unpaired device has no durable store identity, every reconnect
created another temporary device and another disconnect. This produced an
unbounded reconnect loop, raced later pairing attempts, and emitted repeated
disconnect events.

This is an L3 runtime-lifecycle and external-provider change. It does not alter
the HTTP request or success-response contract, database schema, credentials,
or supported single-active topology.

## Decision

Each process-local WhatsApp runtime owns a one-shot phone-pairing readiness
signal. The runtime closes that signal when its event handler receives the
first `events.QR` event. The Whatsmeow service exposes a context-aware wait that
is bound to the runtime generation; readiness from a replaced generation
cannot release a waiter for the current generation.

The instance pairing service passes the HTTP request context through the wait
and provider command. It bounds readiness waiting to twenty seconds, then
re-reads the active client and verifies that the same connected client remains
current inside the fenced provider-command callback. It does not retry
`PairPhone`, because a network failure may leave the remote outcome unknown.

When a disconnected client has no durable `Store.ID`, its runtime generation is
retired without automatic reconnection. Paired clients retain the existing
single-flight reconnect behavior.

## Alternatives

### Increase the fixed sleep

Any fixed delay remains timing-dependent, slows the normal path, and cannot
prove that the QR protocol state was reached. It was rejected.

### Retry `PairPhone` automatically

The operation changes provider state and has no application idempotency key.
Retrying after an ambiguous failure could create multiple active codes or
increase provider throttling. It was rejected.

### Reconnect every disconnected device

This preserves the previous behavior but an unpaired device has no durable
identity to resume. It was rejected because it recreates the observed storm.

### Use `GetQRChannel` solely for readiness

The current passkey flow intentionally consumes `events.QR` directly because
the dependency's QR channel owns additional timeout, disconnect, and automatic
passkey-confirmation behavior. Reintroducing that owner would race the existing
passkey ceremony. It was rejected.

## Consequences

- Phone pairing follows the provider's documented readiness boundary instead
  of wall-clock timing.
- Request cancellation and readiness timeout bound waiting and goroutine use.
- Stale runtime generations cannot authorize a provider pairing command.
- Unpaired disconnects stop cleanly and require an explicit later pairing or QR
  request to start another runtime.
- A provider disconnect after `PairPhone` admission is returned as an error and
  is not automatically retried.

## Acceptance, rollout, and rollback

Acceptance requires unit tests for delayed runtime installation, QR readiness,
generation replacement, cancellation, idempotent signaling, and paired versus
unpaired identity classification. The complete build, vet, test, and race gates
must pass. The public `/instance/pair` request and success response remain
unchanged.

Deploy the immutable candidate to drained staging only. Pair one unpaired
staging instance and require one code response, no reconnect loop, one auth
device, and unchanged production health before removing the staging drain.
Do not promote the candidate to production as part of this validation.

Rollback by restoring the previous staging image digest and restarting only the
staging API. No database rollback is required. If pairing fails, keep staging
drained, restart its API to retire temporary runtimes, and retain the new
instance row for a later retry.
