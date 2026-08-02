# ADR 0063: Fence application-issued WhatsApp provider commands

- Status: Accepted
- Date: 2026-08-03

## Context

ADR 0062 established a durable process ownership epoch and a shared/exclusive
PostgreSQL side-effect fence. The primitive was intentionally not wired to a
partial set of endpoints: partial fencing would create false confidence while
unfenced provider calls could still escape from a stale process.

The application issues WhatsApp mutations from many domains. The inventory
includes connection lifecycle and pairing, messages and media uploads,
presence and receipts, chat and label app-state patches, group and community
management, newsletter creation and subscriptions, privacy and blocklist
changes, profile changes, calls, passkey ceremonies, campaigns, and automatic
event-handler actions. Returning a raw `*whatsmeow.Client` to those domains
without a command capability made bypass easy and mechanically invisible.

WhatsApp does not accept OmniWA's epoch. Correct admission therefore requires
the epoch validation and the complete provider callback to remain under the
shared database fence described by ADR 0062.

This is an L3 external-side-effect and concurrency change. It does not change
the HTTP API, enable active-active operation, or authorize automatic promotion.

## Decision

The process constructs exactly one `ownership.SideEffectFencer` after the
ownership epoch is activated. Composition passes it to the process-local
runtime registry. A registry without exactly one executor fails closed.

Split the runtime client capability into two interfaces:

- `ClientProvider` permits read-only lookup of an active client;
- `CommandClientProvider` additionally admits bounded provider commands.

Every mutation-bearing domain service depends on `CommandClientProvider`.
Application-issued provider mutations execute through
`runtime.DoProviderCommand` or `runtime.DoProviderCommandValue`. The registry
adds a two-minute maximum deadline and delegates to the epoch fencer. Callback
code must use the supplied `commandCtx`; it must not use a background context
or start detached provider work.

Connection establishment uses whatsmeow's context-aware `ConnectContext`
rather than `Connect`, so cancellation can stop a dial before the database
fence expires. Local `Disconnect` is a deliberate safety-reducing exception:
a stale or database-isolated process must always be able to close its socket,
so teardown does not require command admission. Reconnect first disconnects
locally and then admits only `ConnectContext` through the fence.

Each independently observable provider operation receives its own command
admission. A multi-step business operation, such as applying multiple privacy
settings or sending a sequence of presence updates, is not made transactionally
atomic. A newer epoch may take ownership between steps; the next step then
fails stale before provider code runs.

An architecture test type-checks production packages and identifies methods
whose selected receiver is a whatsmeow client. It rejects inventoried mutation
calls unless they are lexically inside a command-facade callback. Context-aware
methods must receive the callback's `commandCtx`. The test also requires a
minimum matched inventory so a broken type-loading configuration cannot pass
vacuously.

Provider errors retain their existing contract and are not retried by the
facade. If the provider callback succeeds but the database fence cannot be
released cleanly, ADR 0062's `ErrSideEffectOutcomeUnknown` is returned. Callers
must not automatically retry an ambiguous mutation.

## Covered application commands

The enforced inventory covers:

- `ConnectContext`, logout, phone pairing, and passkey responses; local
  disconnect remains an always-available containment operation;
- message sends, media and newsletter uploads, presence, chat presence,
  receipts, reactions, edits, revocations, call rejection, and automatic call
  replies;
- chat and label app-state changes;
- group creation, joining, leaving, participant and join-request changes,
  settings, metadata, photos, community links, and invite-link reset;
- privacy settings, blocklist changes, profile photo/name/status changes;
- newsletter creation and live-update subscription;
- campaign sends because campaigns terminate in the same send/upload facade.

Information queries, local builders, downloads, event-handler registration,
local stores, and proxy object configuration do not alter remote business state
and stay outside the side-effect transaction.

## Invariants and limitations

- No inventoried application-issued whatsmeow mutation may compile outside a
  fenced command callback without failing the architecture test.
- A missing executor, nil context, nil callback, database admission failure, or
  stale epoch fails before provider code runs.
- Local socket teardown remains available after epoch or database loss; it
  cannot establish a connection or issue a business mutation.
- Every context-aware provider mutation uses a context with a maximum two-minute
  deadline. Existing shorter caller deadlines still win.
- A callback occupies one users-database connection and one shared advisory
  lock for its duration. Pool capacity and provider latency must be monitored.
- The fence cannot prove a remote outcome after a network or database failure.
  Existing idempotency keys, unknown-outcome handling, and operator review are
  still required.
- Whatsmeow may emit transport-level protocol acknowledgements internally as
  part of maintaining a connection. Those library internals are not
  application-issued business commands and cannot carry an OmniWA epoch.
- Complete command admission removes one automatic-promotion blocker, but it
  does not prove split-brain safety. Inbound duplication, connection lifetime,
  routing, drain timing, and repeated failure drills still require evidence.

## Alternatives

### Check the epoch in every service

This repeats policy, leaves a check/call race, and cannot be enforced reliably.
It was rejected.

### Wrap only send-message endpoints

Presence, receipts, group changes, pairing, automatic replies, and connection
establishment would remain stale-owner side effects. Partial coverage was
rejected.

### Hide the complete whatsmeow client behind one method per provider method

This would duplicate a large and fast-moving upstream API and produce frequent
fork-sync conflicts. The narrow typed command executor centralizes admission
while the architecture test prevents raw mutation bypasses.

### Hold the process ownership connection during commands

It would serialize unrelated work, block ownership monitoring, and couple a
single critical connection to provider latency. The separate shared transaction
fence from ADR 0062 preserves concurrency.

## Rollout, observability, and rollback

There is no schema or public API change. Deploy with the existing stop-first,
single-active topology. Verify successful epoch activation before clients
connect, normal command latency, users-database pool headroom, absence of stale
epoch errors, and no increase in ambiguous provider outcomes.

Rollback uses the previous application image and the same database. Migration
41 and its epoch row remain in place and must not be deleted or decremented.
Rollback does not authorize starting two active images.

Before automatic promotion is considered, run repeated controlled split-brain
drills that hold old callbacks, activate a newer epoch, submit stale commands,
exercise connection loss, and verify that provider callbacks are not invoked
after stale rejection. Record RTO, duplicate inbound events, ambiguous outcomes,
database pool saturation, and drain completion. A later ADR must evaluate that
evidence and explicitly authorize or reject automatic promotion.

The isolated PostgreSQL evidence gate is specified in
`docs/runbooks/ownership-fence-validation.md`. It repeats held-callback epoch
transitions, terminates a fenced transaction connection to prove typed unknown
outcomes, and saturates a bounded command pool. Passing that gate does not
cover provider sockets, inbound duplication, traffic control, or end-to-end
RTO, so it does not change the automatic-promotion decision.
