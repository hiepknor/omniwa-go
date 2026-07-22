# ADR 0016: Instance runtime ownership and fencing

## Status

Accepted

## Context

WhatsApp clients, kill channels, event handlers, and reconciliation loops were
kept in shared maps passed through multiple services. HTTP handlers,
event callbacks, reconnect goroutines, and shutdown paths can read and mutate
those maps concurrently. A mutex around each map would prevent a Go runtime
panic but would not prevent an old reconnect or disconnect operation from
removing a newer client.

The same in-memory design cannot safely provide multi-replica ownership. Two
replicas may connect the same instance, multiply per-instance rate limits, or
claim campaign work on a process that does not own the socket.

## Decision

Introduce an instance runtime registry as the sole owner of in-process runtime
state. A runtime has an immutable generation/fencing value and owns its client,
cancellation context, event handlers, and background loops. Registry operations
must support atomic install, lookup, start/reconnect single-flight, and
remove-if-current semantics.

Domain services receive a narrow client provider interface. They never access
or delete runtime maps directly. Cleanup is idempotent and removes a runtime
only when its generation still matches.

Until distributed ownership is implemented, the supported deployment topology
is one application replica. Documentation and deployment examples must not
claim active-active support.

The process enforces this topology with a database-scoped PostgreSQL advisory
lock held on a dedicated users-database connection. Lock contention fails
startup before migrations, workers, listeners, or WhatsApp connections begin.
Loss of that connection triggers graceful shutdown. This guard contains
accidental duplicate deployments but does not provide per-instance failover or
remove the need for fenced distributed leases.

When multi-replica operation is accepted as product scope, add a PostgreSQL
instance-owner lease with a fencing generation. Only the lease owner may open a
WhatsApp connection, admit queries, or claim outbound work for that instance.
Cross-replica realtime delivery then uses a shared event bus.

## WebSocket ownership

WebSocket connections are not stored as a single connection per instance.
Each connection receives a session identity, a bounded outbound queue, and one
write pump. Network writes never occur while holding the global hub lock. Slow
consumers are disconnected instead of blocking unrelated instances.

## Consequences

- In-process races and stale-cleanup bugs become testable registry invariants.
- Rate guards remain meaningful because one runtime owns each instance.
- The initial registry change is broad but does not require a database schema.
- Multi-replica support remains a later additive lease migration.

## Rollout and rollback

Characterization and race tests landed before callers moved. All raw client,
state, and kill-channel maps have now been removed. Runtime construction is
single-flight per instance, publication precedes event delivery, failed starts
retire only their own generation, and explicit disconnect never recursively
starts a replacement. An architecture test prevents raw client-map ownership
from returning.

WebSocket sessions have also moved from one replaceable connection per instance
to independent session identities. Each session owns a bounded queue and one
write pump; producers only enqueue immutable frames after releasing the hub
lock. A full queue disconnects that session without delaying other consumers.

Before distributed leases exist, rollback is application-image rollback and
the single-replica invariant remains mandatory.
