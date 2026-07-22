# ADR 0011: Campaign recipient leases and completion fencing

## Status

Accepted

## Context

Campaign workers may run on multiple replicas, restart during a send, or lose a
database connection after WhatsApp accepts a message. Unbounded claims and
unfenced completion would create duplicate work and inconsistent recipient
history.

## Decision

Claim at most 100 ready recipients in one PostgreSQL statement using
`FOR UPDATE SKIP LOCKED`. Only recipients belonging to a running campaign are
eligible. Pending recipients must be due; processing recipients become eligible
only after their lease expires. Each claim receives a new opaque token and
lease. Claims take a key-share lock on the campaign row so they serialize with
pause, abort, and other lifecycle transitions; a claim cannot commit based on a
stale running state after a lifecycle change wins the lock.

Sent, retry, and terminal-failure updates compare the recipient identity,
processing status, and claim token. The state update and recipient audit event
commit in one transaction. A stale worker therefore receives a claim-lost error
and cannot overwrite a newer worker's result. Retries persist a future schedule
and accept only bounded, non-sensitive error codes.

Pausing or aborting prevents new claims. An already leased send is considered
in flight and may finish; its lease is not revoked because doing so could allow
a second worker to send the same recipient concurrently. The public contract
must document this boundary. Campaign completion is rejected while any
recipient remains pending or processing.

Draft creation is capped at 10,000 recipients per transaction. Larger workloads
require an explicitly designed import/batching contract rather than an
unbounded request.

## Consequences

- Multiple replicas can claim disjoint work without blocking one another.
- Expired work is recoverable after a crash.
- A lease reduces duplicate risk but cannot prove exactly-once delivery across
  the external WhatsApp boundary; provider message IDs and reconciliation remain
  necessary.
- Worker implementations can remain small and use these repository invariants
  instead of reimplementing coordination.
