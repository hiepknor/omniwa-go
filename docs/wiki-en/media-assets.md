# Shared media asset foundation

OmniWA GO uses one private, instance-scoped media asset domain for campaign and
chat images. Migration 26 creates the additive storage foundation. Migration 27
backfills campaign metadata and references without moving existing objects.

## Configuration

Keep the feature disabled while applying the migration:

```env
WA_MEDIA_ASSETS_ENABLED=false
WA_CHAT_IMAGE_CONTENT_ENABLED=false
MINIO_ENABLED=true
MEDIA_ASSET_BUCKET=omniwa-media-assets
MEDIA_ASSET_MAX_BYTES=8388608
MEDIA_ASSET_MAX_PIXELS=16000000
MEDIA_ASSET_UNBOUND_TTL=24h
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

## Device upload and outbound chat image

Enable the outbound chat stage only after the shared bucket is provisioned:

```env
MINIO_ENABLED=true
WA_MEDIA_ASSETS_ENABLED=true
WA_CHAT_IMAGE_CONTENT_ENABLED=true
```

Upload one JPEG or PNG from a device. The server detects the bytes, enforces
the configured byte and decoded-pixel limits, strips source metadata by
re-encoding, and stores only the normalized canonical object.

```http
POST /media-assets
apikey: <instance-token>
Idempotency-Key: <client-generated-key>
Content-Type: multipart/form-data

file=<binary image>
```

The response contains safe metadata under `data`, including the opaque asset
ID, status, canonical MIME type, dimensions, size, and SHA-256. It never
contains an object key, bucket, filename, provider descriptor, URL, or bytes.
`GET /media-assets/{mediaId}` reads metadata and
`DELETE /media-assets/{mediaId}` removes an unreferenced asset. All operations
are scoped to the authenticated instance; a cross-instance lookup is returned
as not found. Assets with a campaign rollback shadow must be deleted through
`/campaign-media`; the generic delete endpoint refuses them while the rollback
window remains open.

Send the uploaded image through the existing `POST /send/media` route:

```json
{
  "number": "120363000001@g.us",
  "type": "image",
  "mediaAssetId": "927beb51-46c2-4331-b3b4-d96f67280bd3",
  "caption": "Branch update"
}
```

`mediaAssetId` cannot be combined with `url`, and this stage accepts only
normalized JPEG or PNG images. Captions are limited to 1,024 Unicode code
points. Existing JSON URL/base64 and multipart `/send/media` requests are not
changed.

Before contacting WhatsApp, the server fences the asset with a message
reference, rereads the private object, and verifies its size and SHA-256. It
performs one provider upload and one send attempt. A failure known to precede
send admission releases the reference. If acknowledgement is lost, the API
returns `unknown_send_outcome`; clients must not retry automatically because
the original image may already have been sent.

Message references use `WA_MSG_RETENTION`. After a reference and the unbound
asset TTL have both expired, the cleanup worker claims the asset with a lease,
deletes its private variants, and marks it deleted. Explicit deletion returns
`media_asset_conflict` while any active reference exists.

Important errors are `invalid_media_asset`, `media_asset_too_large`,
`unsupported_media_asset_type`, `invalid_media_asset_dimensions`,
`media_asset_not_found`, `media_asset_not_ready`,
`media_asset_integrity_failed`, `media_asset_storage_unavailable`, and
`unknown_send_outcome`.

When enabled and the messages projection is serving, the server advertises
`chat_image_content`. Disable `WA_CHAT_IMAGE_CONTENT_ENABLED` to roll back this
stage. The upload routes and `mediaAssetId` sender are removed, while legacy
media sends and stored shared assets remain unchanged.

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
campaign images can be read. Metadata `/media-assets` and outbound chat assets
are available only behind their independent chat flag. Inbound capture,
download workers, and authenticated media content streaming remain later
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
