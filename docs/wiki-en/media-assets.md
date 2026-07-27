# Shared media asset foundation

OmniWA GO uses one private, instance-scoped media asset domain for campaign and
chat images. Migration 26 creates the additive storage foundation. Migration 27
backfills campaign metadata and references without moving existing objects.

## Configuration

Keep the feature disabled while applying the migration:

```env
WA_MEDIA_ASSETS_ENABLED=false
MINIO_ENABLED=true
MEDIA_ASSET_BUCKET=omniwa-media-assets
```

Provision `MEDIA_ASSET_BUCKET` before enabling the flag. It must be private and
must differ from the legacy `MINIO_BUCKET`. Startup checks that the bucket
exists and is reachable, but it never creates the bucket or changes its policy.

`CAMPAIGN_MEDIA_BUCKET` remains supported during the campaign rollback window.
If `MEDIA_ASSET_BUCKET` is omitted and an explicit campaign bucket is present,
that private campaign bucket is used as a compatibility fallback. A fresh
configuration defaults to `omniwa-media-assets`.

## Security boundary

New object keys contain only server-generated instance and asset UUIDs. The
storage adapter accepts only canonical and provider-original JPEG/PNG variants.
It does not return public URLs. Bucket names, object keys, provider descriptors,
user filenames, and binary payloads are excluded from public models and audit
records.

The legacy MinIO bucket may be public for compatibility. Never configure the
shared media bucket to use that bucket. Verify anonymous `GetObject` is denied
as part of provisioning and deployment acceptance.

## Campaign compatibility rollout

Migration 27 preserves each legacy campaign asset ID, state, expiry, immutable
metadata, and object key. Ready assets gain a canonical variant and campaigns
gain retention references. The migration is replay-safe and deliberately does
not add a campaign foreign key to the shared table, so the previous binary can
still run during the rollback window.

When both `WA_MEDIA_ASSETS_ENABLED` and
`WA_CAMPAIGN_IMAGE_CONTENT_ENABLED` are enabled, new campaign uploads write the
shared record and a transactional `campaign_media_assets` rollback shadow. New
objects use the shared bucket and namespace. A routed storage adapter continues
to read and delete backfilled `campaign-media/.../image` objects from
`CAMPAIGN_MEDIA_BUCKET`; operators do not copy or rename objects during this
stage.

Enable the shared path only after checking the migration counts, provisioning
the private shared bucket, and verifying that representative legacy and new
campaign images can be read. Public `/media-assets`, outbound chat assets,
inbound capture, download workers, and media streaming are delivered in later
staged changes.

Rollback by disabling `WA_MEDIA_ASSETS_ENABLED`; campaign traffic returns to
the maintained legacy metadata and bucket. Keep both additive tables and both
buckets intact. Do not remove migration 27 rows or move objects during rollback;
use a forward migration for schema corrections.

Instance deletion is fail-closed. The service deletes every planned private
variant before deleting the corresponding fenced metadata set and instance in
one database transaction. If storage is unavailable or a concurrent asset is
created, deletion fails and can be retried safely. Keep MinIO and both private
buckets reachable when deleting an instance that has media, even during a
feature rollback.
