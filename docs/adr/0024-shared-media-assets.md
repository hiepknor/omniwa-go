# ADR 0024: Shared private media assets

## Status

Accepted. Migration 26 and the generic storage boundary are an additive,
disabled-by-default foundation. Campaign and chat cutovers are separate staged
changes.

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

Migration 26 is expand-only. It does not backfill campaign rows, move objects,
start workers, advertise capabilities, or change public APIs. The
`WA_MEDIA_ASSETS_ENABLED` flag defaults to false. Enabling this foundation only
validates configuration and private bucket health; consumer behavior remains
unchanged until later PRs.

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
deletion must gain an object purge workflow before any shared consumer writes
production rows. This deliberately fails closed instead of deleting metadata
and orphaning private objects.

## Rollout and rollback

Apply migration 26 with `WA_MEDIA_ASSETS_ENABLED=false`. Provision the dedicated
private bucket, verify anonymous reads fail, then enable only in development or
staging to exercise startup health. Disable the flag to roll back the runtime;
leave additive tables and objects in place. Repair schema defects with a
forward migration. Do not reuse or make the bucket public.

Backfill, campaign cutover, outbound chat, inbound capture, and destructive
legacy removal each require their own rollout and rollback gates.
