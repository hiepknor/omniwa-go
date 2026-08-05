# ADR 0069: Deprecate the unbounded provider-backed group listing

- Status: Accepted
- Date: 2026-08-05

## Context

`GET /group/myall` queries the WhatsApp provider and returns every group in one
response. Its latency, provider load, memory use, and response size therefore
grow without an application bound. `GET /group/search` already provides the
same directory use case from the persisted projection with a limit of 1–200,
an opaque cursor, projection freshness metadata, and the existing information
query safeguards.

Removing the legacy endpoint immediately would break existing clients. Keeping
it indefinitely would preserve an avoidable production exhaustion path.

## Decision

Mark `/group/myall` deprecated on every response using the RFC 9745 structured
`Deprecation` date, an RFC 8594 `Sunset` date of 2027-02-01, and a `Link` to the
bounded `/group/search` successor. Swagger marks the operation deprecated.

`WA_LEGACY_GROUP_MYALL_ENABLED` defaults to `true` for compatibility. Setting it
to `false` returns HTTP 410 before instance lookup or any provider call. The
successor keeps its existing `q`, `limit`, and `cursor` contract.

## Rollout and rollback

Inventory callers from access logs, migrate them to `/group/search`, and verify
cursor traversal plus projection freshness handling. Disable the legacy path in
staging, then production, before the sunset date. Re-enable the flag and redeploy
for an emergency rollback; no data migration is involved.

## Consequences

Clients receive machine-readable migration signals without an immediate break.
Operators gain a zero-provider-call kill switch. The compatibility window still
permits unbounded calls, so rate-limit monitoring and caller migration remain
required until the flag is disabled.
