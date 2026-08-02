# ADR 0058: Own the active runtime lifecycle

- Status: Accepted
- Date: 2026-08-02

## Context

ADR 0056 selected an active-passive modular monolith, and ADR 0057 introduced
explicit process roles. The composition root still creates the application
context and worker supervisor separately, starts one startup task outside the
supervisor, and performs role transitions independently from cancellation and
worker waiting.

That split permits lifecycle drift. In particular, shutdown currently marks a
process terminated after a worker timeout even though work may still be
running. A future standby promotion also needs one boundary that constructs all
active-only handlers, WhatsApp clients, and background work exactly once.

This is an L3 concurrency and lifecycle change. It does not alter the database,
public HTTP contracts, event guarantees, or ownership-lock protocol.

## Decision

`pkg/bootstrap.ActiveRuntime` owns the active application context, supervisor,
and process-role transitions. Its builder receives the runtime context and
supervisor, constructs the HTTP handler, and registers active-only work. Start
is a single attempt and changes the role to `active` only after the builder has
succeeded.

Draining is idempotent and ordered as follows:

1. change the role to `draining`, making readiness fail;
2. cancel the active context;
3. seal worker registration;
4. wait for all supervised work within the caller's deadline;
5. change the role to `terminated` only after every worker has exited.

A timed-out stop remains in `draining` and can be retried with a new context.
The startup instance-connection scan is registered with the supervisor; the
instance runtime registry continues to own the clients it starts and closes
them when the active context is cancelled.

The database ownership guard remains a separate fencing boundary. Its monitor
begins runtime draining immediately on ownership loss and also notifies the
composition root to stop the HTTP server and license heartbeat.

## Alternatives

### Keep lifecycle calls in `main.go`

This preserves fewer types but keeps readiness, cancellation, worker sealing,
and termination as independently ordered operations. It was rejected because
the invalid terminated-on-timeout state is easy to reintroduce.

### Put lifecycle behavior into the worker supervisor

The supervisor does not own HTTP construction or process roles. Expanding it
would mix worker accounting with application state and make later standby
promotion harder to model.

### Mark terminated when the shutdown deadline expires

This gives a clean-looking state even when goroutines remain alive. It was
rejected because liveness and readiness would report a false process state.

### Add automatic standby promotion in this change

Promotion needs migration isolation, deployment wiring, and stronger external
fencing. Combining it with lifecycle extraction would make rollback and failure
diagnosis substantially harder.

## Consequences

- Active-only construction has one concurrency-safe, testable owner.
- Readiness becomes false before active work is cancelled.
- A stuck worker is visible as a process remaining in `draining` until the
  operating system ends it; it is never reported as terminated prematurely.
- Startup may reject late worker registration after concurrent ownership loss.
- License heartbeat shutdown remains composed in `main.go` because `pkg/core`
  is an intentionally isolated and restricted package.

## Acceptance, rollout, and rollback

Acceptance requires tests for concurrent single-start, build failure cleanup,
idempotent drain, stop timeout and retry, worker-registration sealing, and the
full race suite. Existing `/server/ok`, `/server/live`, `/server/ready`, route,
configuration, and database behavior must remain compatible.

Deploy one passive candidate with existing health routing unchanged, verify
startup and graceful shutdown logs, then exercise a controlled termination and
ownership-loss drill before adopting the lifecycle in standby promotion work.
Roll back by restoring the previous immutable image or reverting this focused
commit. There is no schema or persistent-data rollback.
