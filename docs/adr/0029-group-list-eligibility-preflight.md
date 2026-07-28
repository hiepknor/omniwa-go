# ADR 0029: Projection-only Group List eligibility preflight

## Status

Accepted. Rollout is controlled by `WA_GROUP_LIST_ELIGIBILITY_ENABLED` and the
`group_list_eligibility` capability.

## Context

Console operators need to see whether candidate groups are targetable before
submitting a Group List or Campaign. The existing eligibility implementation is
already the authority for Group List mutations and Campaign group targets. A
second client rule set or a live WhatsApp query would create inconsistent
decisions, provider load, and unsafe retry behavior.

An aggregate may evaluate 10,000 list entries. The previous repository path
loaded every participant in every requested group even though eligibility only
needs the connected instance's participant row. On a representative 200,000-row
plan, the existing group-first index narrowed requested groups but left alias
columns as a filter. Identity-first indexes are therefore required for the
aggregate path.

## Decision

The backend exposes two instance-token endpoints:

- `POST /group-lists/eligibility` accepts 1 to 100 unique canonical `@g.us`
  identities and preserves request order.
- `GET /group-lists/{groupListId}/eligibility` evaluates the complete current
  list, bounded to 10,000 entries. Optional `expectedVersion` rejects a mismatch
  before evaluation; historical entries are not reconstructed.

Both endpoints call the existing Group List eligibility service and read only
the persisted groups projection. They never call WhatsApp, write audit events,
send messages, or schedule retries. Unsafe projection state returns HTTP 200
with per-group `unknown / projection_not_ready / canSend:false` and projection
metadata. This makes preflight advisory while preserving backend revalidation
at Group List mutation, Campaign snapshot creation, activation, worker claim,
and provider send.

`GET /group-lists/{id}/groups` returns the same projection metadata on every
page. The aggregate endpoint is on demand and is not added to list-directory
rows, avoiding N+1 work.

Create/update Group List and group-list Campaign creation collect every
non-eligible result within their existing transaction and return a public-safe
issue envelope. At most 100 issues are returned; `issueCount` reports the full
count and `truncated` reports omission. Eligible entries, participant data,
credentials, raw evidence, and provider errors are never included. Invalid or
duplicate input wins first, then a version conflict where applicable, then any
unknown issue returns `503 projection_not_ready`, otherwise unavailable issues
return `409 group_list_group_unavailable`.

The projection repository selects only live participant rows whose
`participant_id`, `phone_number_jid`, or `lid` matches the canonical instance
identity. Migration 29 adds partial identity-first indexes for those three
columns. No Group List eligibility aggregate is cached because projection
freshness and list versions already define the serving boundary.

Prometheus records request latency, requested group count, result counts, and
mutation rejection code. Labels are fixed allowlists; group JID, instance ID,
and arbitrary reasons are not labels.

## Rollout and rollback

Deploy with both Group Lists enabled and eligibility preflight disabled. Apply
migration 29 before enabling the endpoint. Because the migration runner uses a
transactional regular `CREATE INDEX`, index construction can block writes to
the participant projection; schedule a maintenance window based on production
table size and monitor locks and migration duration.

After PostgreSQL integration, query-plan, API contract, metrics, and regression
checks pass, enable the flag for a canary client cohort and verify latency and
unknown-result rates. Then expand rollout. Roll back serving immediately by
disabling `WA_GROUP_LIST_ELIGIBILITY_ENABLED`; existing Group List and Campaign
safety remains active. Leave the additive indexes installed. Any schema
correction uses a forward migration.

## Consequences

- Console gets a stable advisory contract without becoming an authorization
  authority.
- Mutations retain point-in-time transactional revalidation and workers retain
  later recipient revalidation, so a preflight result is never a send promise.
- Large aggregates avoid loading unrelated participant data.
- Index deployment requires explicit operational scheduling on large tables.
- Projection incompleteness is visible as `unknown`, never misreported as lost
  access.
