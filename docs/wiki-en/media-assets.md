# Shared media asset foundation

OmniWA GO is introducing one private, instance-scoped media asset domain for
campaign and chat images. Migration 26 is only the additive storage foundation;
it does not switch existing campaign or chat behavior.

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

## Rollout state

With the feature disabled, migration 26 is inert and existing campaign image
behavior remains on `campaign_media_assets`. Enabling the foundation performs a
fail-closed bucket health check only. Public `/media-assets`, outbound chat
assets, inbound capture, download workers, and media streaming are delivered in
later staged changes.

Rollback by disabling `WA_MEDIA_ASSETS_ENABLED`. Keep the additive tables and
private bucket intact; use a forward migration for schema corrections.
