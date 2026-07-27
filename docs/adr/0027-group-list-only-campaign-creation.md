# ADR 0027: Group List-only campaign creation

## Status

Accepted.

## Context

ADR 0022 introduced Group List campaigns behind rollout flags while preserving
new direct-recipient creation by default during client migration. The finalized
product contract requires every new campaign to select exactly one Group List.
Existing direct campaigns must remain readable, auditable, and executable for
compatibility and rollback.

This is an L3 public-contract cutover. It changes the default behavior of
`POST /campaigns`, while deliberately preserving the persisted direct target
model and worker behavior.

## Decision

`WA_CAMPAIGN_DIRECT_CREATE_ENABLED` defaults to `false`. A create request that
supplies `recipients` instead of a `group_list` target returns HTTP 409 with
`campaign_direct_create_disabled`. The emergency flag may be set explicitly to
`true` to restore the retired request shape during rollback, but it is not
advertised in the public create schema.

No data is migrated or deleted. Existing `targetType=direct` campaigns and
recipients continue through the same read, audit, progress, circuit-breaker,
pause, retry, and worker paths. The group JID canonicalizer remains isolated to
the Group List snapshot path.

## Alternatives considered

Deleting direct rows or worker support was rejected because it would destroy
history and make rollback unsafe. Removing the emergency flag immediately was
rejected because a contract rollback may still be required while older clients
are retired. Leaving direct creation enabled by default was rejected because it
would contradict the finalized product contract and allow new incompatible
work to accumulate indefinitely.

## Rollout and rollback

Before rollout, confirm current direct campaigns remain visible and that
nonterminal direct targets can still be claimed. Deploy with the default false,
verify a direct create is rejected, and verify Group List text and image creates
still succeed. Monitor `campaign_direct_create_disabled` responses for stale
clients.

Rollback by setting `WA_CAMPAIGN_DIRECT_CREATE_ENABLED=true` and restarting the
application. This changes only new draft admission. It does not require a
schema rollback and must not delete or rewrite existing Group List campaigns.
