# ADR 0062: Establish durable runtime ownership epochs

- Status: Accepted
- Date: 2026-08-03

## Context

ADR 0016 requires a fencing generation before multi-replica instance ownership,
and ADRs 0056 through 0061 retain a single active process because the existing
PostgreSQL advisory lock is only a process-lifetime containment boundary. The
lock prevents two cooperative application processes from starting together,
but it has no durable generation that later work can carry to a WhatsApp
provider call. A process whose ownership connection fails also has no value
against which a shared side-effect boundary can reject stale work.

WhatsApp does not accept an OmniWA ownership token. A database check followed by
an unguarded provider call therefore has a time-of-check/time-of-use gap. Merely
adding an epoch column, checking it once at request admission, or checking it in
the in-memory client registry would not fence an already-admitted operation.

This is an L3 distributed-state and database change. It is the first deployable
slice of provider-operation fencing; it does not enable warm active credentials,
in-process promotion, automatic failover, or active-active operation.

## Decision

Add the singleton `runtime_ownership_epochs` table through versioned migration
41. The only supported scope is `application`, and its positive `BIGINT` epoch
is monotonically incremented each time an active process starts. The timestamp
is operational metadata; the epoch is the fencing identity.

The process keeps the existing users-database advisory lock. After all
migrations finish, and before active runtime construction, the guard performs a
single fail-closed activation attempt on the same dedicated PostgreSQL session.
Activation:

1. takes an exclusive transaction-level advisory lock for the global external
   side-effect boundary;
2. inserts epoch 1 or atomically increments the existing epoch;
3. returns the immutable epoch to the active process.

The migration command takes the process ownership lock but never activates an
epoch because it cannot open WhatsApp clients or serve business traffic.
Activation errors terminate startup. The ownership monitor queries the durable
row through the dedicated ownership session and begins draining on connection
failure, missing activation, or epoch mismatch; a successful ping alone is no
longer sufficient.

Provide `ownership.SideEffectFencer` as the shared execution primitive for the
next rollout slice. For one bounded operation it:

1. begins a users-database transaction;
2. takes the shared form of the external-side-effect advisory lock;
3. reads and compares the durable epoch;
4. invokes the provider callback only for the current epoch;
5. holds the shared lock until that callback returns and the transaction ends.

Consequently, activation of epoch N+1 waits for every admitted callback from
epoch N, and a callback submitted with epoch N after activation is rejected
before provider code runs. Callbacks must use the supplied context for every
provider request, remain bounded, and must not detach goroutines. The primitive
does not retry mutations.

If the provider callback succeeds but releasing the database transaction fails,
the executor returns `ErrSideEffectOutcomeUnknown`. Callers must not retry that
mutation automatically because the provider may already have committed it. The
later command facade must map this state explicitly instead of treating it as a
normal pre-admission failure.

The executor is intentionally not wired to a partial selection of endpoints in
this slice. Later focused changes must introduce a narrow WhatsApp command
facade, migrate connection establishment, all user-visible mutations, automatic
receipts/presence/replies, and background campaign operations, and add an
architecture test that rejects direct mutation calls outside that facade.
Automatic promotion remains blocked until that inventory is complete and a
split-brain drill proves the boundary.

## Invariants and limitations

- At most one cooperative process holds the existing process advisory lock.
- Every successful active startup receives an epoch greater than all earlier
  successful active startups for that users database.
- A newer activation cannot pass a correctly used shared side-effect fence
  while an older callback is still running.
- A stale `SideEffectFencer` never invokes its callback after a newer epoch is
  active.
- The epoch and advisory lock do not make WhatsApp itself token-aware. If a
  PostgreSQL connection is forcibly lost while provider bytes are already in
  flight, the provider outcome may still be ambiguous. Stop-first deployment,
  bounded calls, idempotency where available, and receiver deduplication remain
  required.
- Until all raw provider mutation paths use the executor, the presence of an
  epoch row is not evidence that external side effects are fully fenced.

## Alternatives

### Continue using only the process advisory lock

This has no durable generation and cannot reject work admitted by an older
process after ownership changes. It was rejected as insufficient foundation.

### Validate the epoch immediately before each provider call

The epoch can change after the query and before the network write. It was
rejected unless the validation and callback share a lock that newer activation
must acquire exclusively.

### Hold the process ownership session across provider calls

The process lock already lives for the whole runtime and does not distinguish
individual admitted operations. Sharing that single connection would serialize
unrelated commands and couple monitor progress to provider latency. A separate
pool transaction and shared advisory key preserve bounded concurrency.

### Add per-instance leases immediately

Per-instance leases also require routing, cross-replica realtime delivery,
campaign assignment, socket reassignment, and a complete provider facade. The
current product scope is active-passive at users-database granularity, so a
global process epoch is the smallest correct predecessor. Per-instance epochs
remain required before instance sharding or active-active operation.

### Treat the provider message ID as the fencing token

Message IDs can support idempotency for specific send operations but do not
cover group changes, receipts, presence, calls, pairing, connection ownership,
or every provider retry path. They cannot replace runtime ownership fencing.

## Rollout, observability, and rollback

Migration 41 is additive and safe on empty or populated supported databases.
Old binaries ignore the table, so image rollback requires no schema rollback.
Do not delete or decrement the row. A failed migration is forward-fixed through
a later migration; an operator must never edit the epoch to restore service.

Container smoke must prove that the first active creates epoch 1 in an isolated
database, controlled promotion increments exactly once, the migration job does
not increment it, and a later restart increments exactly once again. PostgreSQL
integration tests must prove activation ordering, stale rejection, callback
non-execution, lock exclusion, and monotonicity. Unit and race tests cover
single-attempt activation and monitor failure paths.

Roll out the epoch foundation first with the existing single-replica,
stop-first topology. Alert on `action=activate_epoch` startup failure and any
ownership monitor failure. The next rollout migrates provider mutations behind
the shared executor without changing deployment topology. Only after complete
mutation coverage, bounded ambiguous-outcome handling, and repeated
split-brain drills may a later ADR authorize automatic promotion.
