# ADR 0067: Separate HTTP draining from worker cancellation

- Status: Accepted
- Date: 2026-08-05

## Context

The active runtime previously made the process unready and cancelled all
background workers in one operation, before `http.Server.Shutdown` completed.
An in-flight request could therefore lose a worker or shared runtime dependency
while it was still completing. The single ten-second context also gave HTTP and
workers no independent bounded drain windows.

## Decision

Shutdown uses ordered phases:

1. transition to `draining`, making readiness fail and sealing new worker
   registration;
2. drain in-flight HTTP requests for at most 30 seconds;
3. cancel and wait for supervised workers for at most 30 seconds;
4. shut down the optional license runtime and release resources through existing
   deferred cleanup.

Each phase reports a distinct wrapped error. Production Docker and Swarm
references use a 75-second stop grace period so the orchestrator deadline is
longer than the two application drain windows.

## Rollout and rollback

Deploy with stop-first single-replica replacement and send SIGTERM under a mix
of short and long requests. Verify readiness becomes 503 before connections
close and that the process exits without the orchestrator sending SIGKILL.

Rollback reverts to the prior runtime shutdown ordering; there are no persistent
data or public API changes. If termination exceeds the platform budget, first
reduce request duration or raise the platform grace period rather than removing
the worker-after-HTTP safety boundary.
