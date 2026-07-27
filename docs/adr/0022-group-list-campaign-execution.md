# ADR 0022: Group List campaign snapshots and safe group execution

## Status

Accepted and implemented behind `WA_CAMPAIGN_GROUP_TARGETS_ENABLED`. The
`campaign_group_targets` capability remains absent unless both Group Lists and
the complete group execution path are enabled.

## Context

The current campaign contract stores direct-user recipients with consent
evidence, durable leases, bounded retries, and an instance-wide outbound token
bucket. Group campaigns have different authorization and failure semantics. A
group is one delivery target rather than a set of members, Group List edits must
not mutate a running campaign, and a provider acknowledgement must not be
interpreted as delivery to every member.

The current worker also treats most send failures as one retryable
`send_failed` class, while the shared text service performs its own connection
retries. That is insufficient for terminal group failures and unsafe when the
provider may have accepted a message before the outcome became unknown. Group
execution therefore requires an explicit provider boundary, durable group
coordination, and a single authoritative retry policy.

## Decision

### Product boundary

A Group List campaign is text-only and selects exactly one Group List. Each
group is one execution target; the backend never expands a group into members.
Images, uploads, captions, arbitrary provider payloads, and multi-list campaigns
are out of scope. Campaign directory and inspector responses include progress;
footer and activity-dock UI contracts remain out of scope.

### Additive target model and compatibility

Campaign storage gains a non-null `target_type`. Existing rows are backfilled as
`direct`; new Group List campaigns use `group_list`. The campaign row stores the
source Group List UUID, name, and version snapshot. Each group execution row
stores `target_type = group`, canonical `recipient_jid`, and `target_label`.
Legacy direct rows remain readable, executable, and auditable without synthetic
Group List data.

Campaign detail exposes the immutable source target:

```json
{
  "target": {
    "type": "group_list",
    "groupListId": "uuid",
    "groupListName": "Northern branches",
    "groupListVersion": 4,
    "targetCount": 120
  }
}
```

Target pages expose the execution identity and safe outcome without leases or
claim tokens:

```json
{
  "id": "uuid",
  "targetType": "group",
  "recipientJid": "120363000001@g.us",
  "targetLabel": "HCM Branch",
  "status": "sent",
  "attemptCount": 1,
  "lastErrorCode": null
}
```

The group creation contract is:

```json
{
  "name": "July campaign",
  "text": "Campaign content",
  "target": {
    "type": "group_list",
    "groupListId": "uuid",
    "groupListVersion": 4
  }
}
```

Clients cannot submit group JID arrays. The legacy direct-recipient request was
accepted during the compatibility window behind an explicit rollback flag.
ADR 0027 completes the cutover by disabling that request by default. Disabling
legacy creation never prevents reading, auditing, or completing existing direct
campaigns.

The existing direct-JID canonicalizer remains unchanged. A separate group-target
canonicalizer accepts only non-empty WhatsApp `@g.us` identities and is callable
only from the Group List snapshot path.

### Atomic draft snapshot

Draft creation runs in one PostgreSQL transaction:

1. lock the non-deleted Group List;
2. verify instance scope and requested version;
3. reject an empty list;
4. evaluate every entry using ADR 0021 eligibility;
5. copy list identity, name, version, group JIDs, and current group names; and
6. create exactly one unique execution target per group.

Unknown eligibility aborts with HTTP 503 `projection_not_ready`. An unavailable
entry aborts with HTTP 409 `group_list_group_unavailable`. No partial draft is
created. Later Group List updates or deletion cannot change the campaign or its
execution targets.

### Revalidation boundary

Eligibility is checked at draft creation, start, worker claim, and immediately
before the provider call. Start evaluates the snapshot as one operation:

- all eligible targets transition to running;
- unavailable targets become `skipped` with their stable reason and audit event,
  while eligible targets run;
- no eligible targets returns HTTP 409 `campaign_no_eligible_targets`; and
- any unknown target returns HTTP 503 `projection_not_ready` without starting
  or partially skipping the campaign.

Claim and provider-boundary revalidation use the same evaluator. A newly
unavailable target is skipped without consuming an attempt. Unknown projection
state releases or defers the claim without consuming an attempt and pauses the
campaign with `pauseReason = projection_not_ready`. Adding the group back later
does not revive a skipped or failed target.

### Provider result and retry taxonomy

Campaign delivery uses a narrow, context-aware provider interface that performs
one send attempt and no hidden retry. It returns typed outcomes that preserve
whether the send was definitely rejected before acceptance, acknowledged, or
has an unknown result. Provider text and payloads are never persisted or logged.

These errors are terminal and never retried:

- `group_access_lost`
- `group_dissolved`
- `send_permission_denied`
- `group_suspended`
- `invalid_group_jid`

Known transient pre-acceptance failures use bounded exponential backoff with
injectable jitter, `maxAttempts`, and persisted `nextAttemptAt`. Provider 429
uses the complete bounded `Retry-After` value and opens the durable instance
campaign circuit. Deferrals caused by an open circuit or local pacing do not
consume an attempt.

An unknown send outcome is never automatically retried. The target becomes
`failed` with `lastErrorCode = unknown_send_outcome`, the campaign is paused with
the same reason, `needsAttention` becomes true, and both changes are audited in
one transaction. This choice keeps the public progress buckets stable while
requiring an explicit operator decision before any new campaign activity.

### Durable rate protection

Database coordination is authoritative; process-local locks are insufficient
even though the currently supported topology is one application replica.

- `(campaign_id, recipient_jid)` remains unique for every campaign.
- A durable group-delivery guard admits at most one active leased send for an
  `(instance_id, group_jid)` pair.
- The same guard records the last acknowledged send time and enforces the
  configured cooldown across campaigns.
- The existing outbound token bucket remains the instance-wide pacing boundary
  for all interactive and campaign sends.
- A durable instance campaign circuit stores its open-until time and safe cause.
  While open, no campaign for that instance may claim outbound work.
- Campaign-level failure, rate-limit, and authentication thresholds are backend
  policy. Crossing a threshold atomically pauses the campaign and records a safe
  reason. Frontends display the result and never reproduce policy constants.

Leases, guard ownership, cooldowns, and circuit transitions use normalized UTC
timestamps, bounded durations, and fenced claim identities. Crash recovery may
reclaim an expired guard, but an expired or lost claim cannot overwrite a newer
outcome.

### Progress contract

`GET /campaigns` and `GET /campaigns/{id}` use the same aggregation and return:

```json
{
  "progress": {
    "total": 120,
    "processed": 72,
    "pending": 47,
    "processing": 1,
    "sent": 68,
    "delivered": 0,
    "read": 0,
    "failed": 2,
    "skipped": 2,
    "aborted": 0,
    "updatedAt": "2026-07-26T10:00:00Z"
  },
  "statusReason": null,
  "pauseReason": null,
  "retryAt": null,
  "needsAttention": false
}
```

The buckets are mutually exclusive current target states. `total` is their sum;
`processed` is `sent + delivered + read + failed + skipped + aborted` and is
never inferred by a client. `retryAt` is the earliest effective time at which
the campaign may be retried, including a later instance-circuit boundary.
`updatedAt` is the newest campaign
or target progress mutation. A campaign completes when it has no pending or
processing targets; completion does not mean every target succeeded.

For group targets, `sent` means the provider acknowledged the send to the group.
`delivered` and `read` remain zero unless a future provider contract supplies a
group-level acknowledgement with defined semantics. Member receipts are never
expanded, counted, or implied. Failed and aborted campaigns retain their actual
progress instead of being rewritten as all failed.

### Capability and public errors

The instance-scoped `campaign_group_targets` capability requires the complete
Group List stack and capability, current campaign schema, enabled group creation,
eligibility revalidation, provider adapter, durable group guards, circuit policy,
worker support, progress reads, and audit wiring. Partial schema or dormant code
must not advertise it.

New stable campaign errors include:

- `campaign_no_eligible_targets`
- `group_list_not_found`
- `group_list_version_conflict`
- `group_list_group_unavailable`
- `projection_not_ready`

Existing legacy campaign errors and response readability remain compatible.

## Rollout and rollback

Implementation is split into deployable increments:

1. Group Lists and eligibility land independently under ADR 0021.
2. Add nullable/defaulted target snapshot and progress fields, backfill existing
   campaigns to `direct`, verify coverage, and keep group creation disabled.
3. Add provider classification, revalidation, durable group guards, circuits,
   policy tests, and group worker support with
   `WA_CAMPAIGN_GROUP_TARGETS_ENABLED=false` and the capability absent.
4. Enable Group List creation in a canary environment, advertise the capability
   there, move the Console, and observe send, skip, pause, retry, circuit, and
   unknown-outcome metrics. Keep `WA_CAMPAIGN_DIRECT_CREATE_ENABLED=true` as the
   compatibility rollback path during this window.
5. Expand serving, then disable legacy direct creation after the agreed
   compatibility window. ADR 0027 records this completed admission cutover;
   destructive cleanup remains a separate future decision.

Each migration is forward-only and safe for an empty or populated database.
Application rollback leaves additive columns and tables in place and retains the
previous image's ability to process direct campaigns. Disabling the serving flag
and capability is the immediate behavior rollback while the group-aware image is
running. A binary rollback after group campaigns exist is gated: disable new
group creation, atomically pause every non-terminal Group List campaign, wait for
or fence active leases and group guards, verify that no Group List campaign is
running, and only then deploy the previous image. An older binary must never be
started while a group target is claimable because its legacy claim query cannot
filter a target type it does not understand.

## Consequences

- Campaign targeting is reproducible even after Group List edits or deletion.
- Direct campaign history remains available throughout rollout and rollback.
- Group delivery is at-least-once only for known retryable pre-acceptance
  failures; unknown outcomes deliberately favor duplicate prevention.
- Durable per-group coordination adds schema and operational complexity but
  makes cooldown and concurrent-campaign guarantees testable.
- The Console can use one progress definition without encoding backend state
  machine or policy knowledge.

## Required verification

Release verification includes populated-schema backfill and rollback tests;
legacy direct read, audit, execution, and contract tests; Group List
snapshot and version-conflict tests; kick, leave, permission, suspension, and
dissolution tests at every revalidation boundary; terminal, transient,
rate-limit, and unknown-outcome worker tests; concurrent campaigns targeting the
same group; cooldown, instance circuit, campaign auto-pause, lease-expiry, and
crash-recovery tests; progress aggregation parity between list and detail; audit
privacy tests; deterministic Swagger regeneration; and the repository build,
vet, test, race, PostgreSQL, container smoke, and secret-scan gates.
