# ADR 0024: Shared private media assets

## Status

Accepted. Migration 26 and the generic storage boundary are the additive,
disabled-by-default foundation. Migration 27 backfills and cuts campaign
metadata over behind feature flags while retaining a rollback shadow. Chat
cutovers remain separate staged changes.

## Context

Campaign images currently use a durable private asset implementation while the
legacy chat media path uses request-lifetime byte slices and a public-URL
storage contract. Reusing either contract unchanged would couple chat media to
campaign retention or expose private media through the legacy bucket policy.

This is an L3 decision because it introduces shared persistent state, encrypted
future download work, cleanup concurrency, retention ownership, and an external
object-store boundary. It must preserve rollback to the campaign-specific
implementation until a later release closes that window.

## Decision

`media_assets` is the shared instance-scoped identity and lifecycle record.
Binary metadata and server-generated private object keys live in
`media_asset_variants`. A device upload has a canonical rendition; a future
live inbound image may additionally retain a provider-original variant for
hash verification. Campaigns and projected messages own retention through
`media_asset_references` rather than object keys.

The foundation also reserves durable, leased `media_download_jobs` and
secret-free `media_asset_audit_events`. Provider descriptors must be encrypted
before a later inbound-capture stage writes a job. Public responses never
contain object keys, provider descriptors, filenames, bucket names, or URLs.

New objects use this exact namespace:

```text
media-assets/{instance UUID}/{asset UUID}/{canonical|provider_original}
```

The generic MinIO adapter validates every segment, accepts only JPEG and PNG,
never mutates bucket policy, and never creates a public or presigned URL.
`MEDIA_ASSET_BUCKET` must be a dedicated private bucket and must differ from
the legacy `MINIO_BUCKET`.

Migration 26 is expand-only. Migration 27 preserves campaign asset identities,
adds canonical variants and campaign retention references, and does not move
objects. During the compatibility window, new campaign writes update shared
metadata and `campaign_media_assets` in one transaction. A routed store sends
new `media-assets/.../canonical` keys to `MEDIA_ASSET_BUCKET` and exact legacy
`campaign-media/.../image` keys to `CAMPAIGN_MEDIA_BUCKET`. No shared campaign
foreign key is added yet because it would prevent rollback to the old binary.

The
`WA_MEDIA_ASSETS_ENABLED` flag defaults to false. Enabling this foundation only
validates configuration and private bucket health. Campaign behavior switches
only when campaign image content is also enabled.

## Alternatives considered

Renaming `campaign_media_assets` was rejected because it removes application
rollback and combines schema expansion with behavior switching. Reusing the
legacy media store was rejected because it can grant anonymous read to its
bucket and does not namespace objects by instance. Storing bytes in PostgreSQL
was rejected because it increases database and backup exposure and defeats
bounded object streaming.

## Consequences

There are temporarily two metadata schemas while the rollback window remains
open. Later stages must backfill campaign assets without moving objects, switch
consumers behind flags, and remove the legacy table only in a separate contract
migration. Shared references make cleanup aware of consumer retention without
giving consumers direct storage access.

The database uses `ON DELETE RESTRICT` from shared assets to instances. Instance
deletion first plans and removes every known private variant, then deletes only
that fenced set of metadata in the same transaction that deletes the instance.
A concurrently-created asset is not in the fence, so the FK rolls the
transaction back. Storage failure also stops deletion. Object removal is
idempotent, which makes retry safe after a database or concurrency failure.

## Rollout and rollback

Apply migrations 26 and 27 with `WA_MEDIA_ASSETS_ENABLED=false`. Validate the
backfill counts and immutable metadata, provision the dedicated private bucket,
verify anonymous reads fail, then enable both media flags in development or
staging. Exercise one backfilled image and one new image before production
rollout. Disable `WA_MEDIA_ASSETS_ENABLED` to roll campaign traffic back to the
legacy shadow. Leave both metadata schemas and both object namespaces in place.
Repair schema defects with a forward migration. Do not reuse or make either
bucket public.

Backfill, campaign cutover, outbound chat, inbound capture, and destructive
legacy removal each require their own rollout and rollback gates.
