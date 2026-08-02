# ADR 0061: Capture controlled failover drill evidence

- Status: Accepted
- Date: 2026-08-03

## Context

ADR 0056 defines a five-minute controlled-failover gate, exclusive ownership,
durable-delivery recovery, and no intentional loss. ADR 0060 provides a
secretless cold standby and a manual promotion runbook, but a human checklist
does not produce consistent timing or prove which safety gates actually ran.

The available signals have different strength. A successful one-shot migration
after the former active stops proves that the migration process acquired the
same PostgreSQL advisory ownership boundary. Process readiness proves that the
new HTTP data plane completed active construction. Outbox aggregate metrics
show replay progress and dead letters. Persisted instance `connected` flags do
not prove that every provider socket reconnected, and an advisory lock does not
fence an already-issued external WhatsApp side effect.

This is an L3 operational and distributed-state change. The drill intentionally
stops the active service. It adds no automatic production failover, schema, or
application API.

## Decision

Provide `scripts/ops/cold-standby-drill.sh` as an explicitly authorized,
fail-closed Compose drill runner. Execution requires:

- the exact approval phrase `STOP_ACTIVE_AND_RUN_CONTROLLED_FAILOVER`;
- a new absolute evidence path, expected 40-character source revision, and an
  API key read from a file rather than a command-line argument;
- explicit loopback HTTP control-plane URLs, so the admin API key is never sent
  to an operator-supplied remote host;
- a bounded RTO, outbox drain timeout, oldest-pending threshold, and poll
  interval;
- an executable operator-owned traffic-drain probe before stopping active;
- an executable operator-owned post-promotion probe that verifies the
  environment-specific reconnect and signed delivery canary.

The runner verifies the current active revision and health, records outbox
baseline aggregates, starts and revalidates a secretless/unready standby, and
runs the traffic probe. It then measures from active stop through these gates:

1. the old active container is no longer running;
2. the immutable standby process is stopped rather than promoted in process;
3. the migration service acquires ownership and completes;
4. the recreated active becomes ready within the RTO;
5. its runtime revision matches the approved revision;
6. pending/processing outbox work makes progress (or remains zero), dead letters
   do not increase, and oldest pending age stays bounded;
7. the operator post-promotion probe succeeds.

Evidence is atomically written as private mode-0600 JSON conforming to
`docs/schemas/failover-drill-evidence-v1.schema.json`. It contains aggregate
counts, timestamps, probe hashes, revision, checkpoints, RTO, and a
`recoveryRequired` flag. It deliberately excludes credentials, endpoints,
instance identifiers, payloads, command output, and logs.

Any failed gate exits nonzero. Once active stop begins, failures retain
`recoveryRequired=true`; the runner does not guess a rollback digest, reopen
traffic, mutate outbox rows, or bypass ownership. Recovery remains an explicit
operator decision under the runbook.

## Alternatives

### Treat readiness alone as a successful failover

Readiness says nothing about durable delivery progress, provider reconnects,
or the approved artifact revision. It was rejected as a false-positive RTO.

### Query `connected=true` from persisted instance rows

That value represents stored desired/history state and can remain true before a
new in-process client is connected. It was rejected as reconnect proof.

### Automatically run a WhatsApp or Webhook mutation from the generic runner

The project cannot choose a safe recipient, consent context, or receiver for
every environment. A hashed, operator-owned executable probe makes that policy
explicit without storing its output in evidence.

### Automatically roll back on any failure

After migration, the compatible prior digest and correct traffic state depend
on deployment history. Guessing can worsen an incident. The runner instead
marks recovery required and leaves traffic closed.

## Consequences and residual risk

- Staging and production drills use the same ordered, bounded gates and produce
  comparable evidence.
- Probe hashes show which scripts ran but do not prove their source review or
  semantics. Operators must version and approve the probe implementations.
- A passing advisory-lock gate still cannot exclude stale external side effects
  already admitted by a partitioned former process. Automatic promotion remains
  blocked on fencing-token work.
- Aggregate outbox progress supports the at-least-once contract; it cannot prove
  exactly-once consumption. Receiver delivery-ID deduplication remains required.
- Evidence provides engineering validation, not a customer-facing SLA.

## Rollout and rollback

Run deterministic fail-path tests in CI and execute the complete runner against
the isolated PostgreSQL container smoke topology. Rehearse in staging with
versioned Caddy drain and post-promotion probes before any production drill.
Retain evidence according to the operator audit policy and never commit it.

Rollback removes the runner and documentation; no runtime or persistent state
changes are required. A failed live drill follows the cold-standby runbook and
the recorded `recoveryRequired` state. Automatic failover may replace this
manual gate only after a later ADR covers fencing, promotion authorization,
traffic-controller concurrency, and split-brain recovery.
