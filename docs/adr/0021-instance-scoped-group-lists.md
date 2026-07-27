# ADR 0021: Instance-scoped Group Lists and authoritative eligibility

## Status

Accepted; implementation is staged and the `group_lists` capability must not be
advertised until the complete read and write stack is available.

## Context

Campaign operators need reusable, named sets of WhatsApp groups. A set cannot be
represented safely by a client-owned array because the server must enforce
instance isolation, retain authorization evidence, detect concurrent edits, and
make one eligibility decision for every caller. The existing group projection
already contains names, participants, roles, announce mode, suspension state,
and tombstones, but ordinary group reads deliberately allow stale projected data
under ADR 0014. Sending requires a stricter freshness boundary than browsing.

A missing group after reconciliation is also different from an explicit provider
deletion. Those facts must remain distinguishable if the API is to return stable
`group_access_lost` and `group_dissolved` reasons instead of guessing from the
absence of a row.

## Decision

### Ownership and persistence

A Group List belongs to exactly one instance and has a UUID, display name,
normalized name, optional description, monotonically increasing version,
authorization metadata, timestamps, and a nullable deletion timestamp. Entries
belong to the same instance and list and contain a canonical `@g.us` JID plus the
group name observed when the entry was written.

The schema uses additive, versioned migrations and enforces:

- a partial unique index on `(instance_id, normalized_name)` for non-deleted
  lists, allowing a name to be reused only after soft deletion;
- a unique constraint on `(group_list_id, group_jid)`;
- a composite instance/list foreign key so an entry cannot cross instance
  scope; and
- a database check that every entry ends in `@g.us`.

Names are normalized by trimming leading and trailing whitespace, collapsing
internal Unicode whitespace to one ASCII space, and applying Unicode lowercase.
The stored display name remains unchanged except for outer whitespace. Names are
limited to 255 Unicode code points and descriptions to 2,000 code points. The
same normalization function is used before every lookup and mutation; database
collation is not the source of truth.

Deletion is soft and increments the list version. Deleted lists are not returned
by normal reads and cannot be updated or selected by a new campaign. Existing
campaign snapshots never cascade from a Group List deletion.

### Authorization evidence and audit

Create and update require an authorization source, evidence reference, and UTC
authorization timestamp that is not in the future. The source is a non-empty
stable identifier of at most 64 bytes and the evidence reference is limited to
4,096 bytes. The raw evidence reference is accepted only long enough to compute
a SHA-256 digest over the list UUID and reference; it is never persisted, logged,
audited, or returned. Scoping the digest to the list prevents correlation of a
reused external ticket across lists.

Every successful create, update, and delete appends an immutable audit event in
the same database transaction. Audit metadata may contain bounded field names,
counts, versions, safe authorization-source identifiers, and request identity;
it never contains raw evidence, API keys, instance tokens, message content, or
provider payloads.

### Mutation and version contract

The instance-token API surface is:

```text
GET    /group-lists?search=&limit=&cursor=
POST   /group-lists
GET    /group-lists/{groupListId}
GET    /group-lists/{groupListId}/groups?limit=&cursor=
PUT    /group-lists/{groupListId}
DELETE /group-lists/{groupListId}
GET    /group-lists/{groupListId}/audit?limit=&cursor=
```

Create and update accept `name`, `description`, `groupJids`, and an
`authorization` object containing `source`, `evidenceReference`, and
`authorizedAt`. Only update accepts and requires `expectedVersion`; accepting it
on create would make client intent ambiguous and is rejected as invalid input.

`POST /group-lists` creates a non-empty list at version 1. `PUT
/group-lists/{groupListId}` is a full replacement of the mutable display fields,
authorization assertion, and entry set. Update requires `expectedVersion`; the
repository locks the list and rejects a mismatch with
`group_list_version_conflict`. A successful material update increments the
version once, regardless of the number of changed entries. A no-op update still
records the authorization assertion and increments the version because it is an
audited operator decision.

Provider-driven group name changes update `currentName` at read time and do not
change the Group List, its entry snapshot, or its version.

List, entry, and audit endpoints use bounded keyset pagination. Every cursor is
opaque, versioned, resource-typed, and scoped by instance, parent list, search
term, and ordering. A cursor from another instance, list, endpoint, or search is
invalid rather than an empty page.

### Eligibility authority

The backend is the only eligibility authority. Clients consume the returned
`eligibility`, `eligibilityReason`, `canSend`, and `checkedAt` values and never
infer permission from group fields.

Eligibility uses a projection snapshot and instance identity read in one service
operation. Browsing semantics from ADR 0014 remain unchanged, but sending
eligibility requires the groups projection to be `ready`, at the current schema
version, and to have a completed reconciliation. `stale`, `syncing`, `failed`,
`not_started`, missing, or structurally incomplete projection state returns:

```text
eligibility = unknown
eligibilityReason = projection_not_ready
canSend = false
```

With a ready projection, reasons are evaluated in this order:

1. An explicit provider group-delete event produces `unavailable /
   group_dissolved`.
2. A group tombstoned because it disappeared from authoritative reconciliation,
   or a ready group that no longer contains the connected instance identity,
   produces `unavailable / group_access_lost`.
3. A suspended group produces `unavailable / group_suspended`.
4. An announce-only group in which the instance is not an admin or super-admin
   produces `unavailable / send_permission_denied`.
5. Otherwise the result is `eligible`, with no reason and `canSend = true`.

Instance identity matching canonicalizes the stored instance JID to a non-device
JID and compares it with each participant's primary JID, phone-number JID, and
LID. A missing or unparseable instance identity, absent required permission
fields, or ambiguous identity mapping is `unknown / projection_not_ready`, not
proof of unavailability.

The group projection will retain a bounded tombstone cause so explicit deletion
can be distinguished from reconciliation loss. Unknown provider reason text is
not exposed; only the stable public reasons above leave the service boundary.

Create and update require every requested entry to be currently eligible. An
unknown result returns HTTP 503 `projection_not_ready`; an unavailable group
returns HTTP 409 `group_list_group_unavailable`; malformed, duplicate, or
non-group identities return HTTP 400 `group_list_invalid_group`. Eligibility may
change later. Such entries remain in the list and are reported rather than being
silently deleted.

### Capability and errors

The instance-scoped `group_lists` capability is advertised only when migrations,
repository, service, handlers, audit, and eligibility wiring are active and the
instance has the ready groups projection required above. Schema presence alone
does not advertise the capability.

The stable public error codes are:

- `group_list_not_found`
- `group_list_name_conflict`
- `group_list_version_conflict`
- `group_list_empty`
- `group_list_invalid_group`
- `group_list_group_unavailable`
- `projection_not_ready`

## Rollout and rollback

The Group List migration is additive. Deploy storage and wiring with
`WA_GROUP_LISTS_ENABLED=false`, then enable the complete stack and capability
only after migration, PostgreSQL integration, instance-isolation, cursor-scope,
audit, and concurrent-version tests pass. The Console must feature-detect
`group_lists`.

Application rollback deploys the previous image and leaves the additive tables
unused. No down migration drops operator lists or audit evidence. A forward fix
is used for schema defects. The capability is the serving kill switch: it is
removed whenever the stack or required projection is not ready.

## Consequences

- Operators get reusable targets without making the Console a permission
  authority.
- Historical entry names and current provider names remain separately useful.
- Explicit dissolution and access loss require projection provenance instead of
  absence-based guessing.
- Strict eligibility may temporarily block mutations while ordinary stale group
  browsing remains available.
- Group List changes are independently auditable and cannot alter an existing
  campaign snapshot.

## Required verification

Implementation is not complete without migration tests for empty, populated,
repeated, and concurrent startup; repository, service, and handler tests;
instance-isolation and cursor-scope tests; concurrent version-conflict tests;
authorization privacy and audit tests; projection freshness and every
eligibility-reason test; deterministic Swagger regeneration; and the repository
build, vet, test, race, PostgreSQL, container smoke, and secret-scan gates.
