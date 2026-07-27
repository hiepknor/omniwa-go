# Campaign media assets

Campaign media assets are immutable, instance-scoped images uploaded from a
device before an image campaign is created. The campaign API accepts their IDs
through the typed image content contract. Upload, creation, transitions,
delivery, cleanup, and capability advertisement share one feature gate.

## Deployment prerequisites

The feature requires PostgreSQL migrations 24 and 25, Group Lists, group
campaign targets, and a dedicated private S3-compatible bucket. The application
verifies that the bucket exists at startup but never creates it or changes its
access policy.

```env
MINIO_ENABLED=true
WA_GROUP_LISTS_ENABLED=true
WA_CAMPAIGN_GROUP_TARGETS_ENABLED=true
WA_MEDIA_ASSETS_ENABLED=true
MEDIA_ASSET_BUCKET=omniwa-media-assets
CAMPAIGN_MEDIA_BUCKET=omniwa-campaign-media
CAMPAIGN_MEDIA_MAX_BYTES=8388608
CAMPAIGN_MEDIA_MAX_PIXELS=16000000
CAMPAIGN_MEDIA_UNBOUND_TTL=24h
WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=true
```

Provision the bucket as private before enabling the flag. Do not reuse a bucket
whose objects are publicly readable. Startup rejects a campaign media bucket
that is the same as the legacy `MINIO_BUCKET`. Keep the flag disabled during migration
and rollback. When the flag is disabled, `/campaign-media` routes are not
registered, image drafts and transitions are rejected, image delivery is not
wired, the cleanup worker is not started, and `campaign_image_content` is not
advertised. Startup fails closed if the image flag is enabled without Group
Lists and group campaign targets.

With shared media enabled, new uploads are authoritative in `media_assets` and
write `campaign_media_assets` as a transactional rollback shadow. Migration 27
backfills legacy assets and campaign references without copying their objects.
The application routes backfilled keys to `CAMPAIGN_MEDIA_BUCKET` and new keys
to `MEDIA_ASSET_BUCKET`, so both private buckets must remain available during
the compatibility window.

## Upload flow

Upload one JPEG or PNG as multipart form data:

```http
POST /campaign-media
apikey: <instance-token>
Idempotency-Key: <client-generated-key>
Content-Type: multipart/form-data

file=<binary image>
```

The server identifies the format from decoded bytes, enforces byte and decoded
pixel limits, and re-encodes the image. This strips source metadata such as
EXIF and prevents a client filename or content-type header from becoming
trusted metadata.

A successful response contains only safe metadata:

```json
{
  "message": "success",
  "data": {
    "id": "927beb51-46c2-4331-b3b4-d96f67280bd3",
    "type": "image",
    "mimeType": "image/jpeg",
    "size": 218341,
    "width": 1600,
    "height": 900,
    "sha256": "e5f6...64-lowercase-hex-characters...",
    "status": "ready",
    "readyAt": "2026-07-27T10:00:00Z",
    "expiresAt": "2026-07-28T10:00:00Z",
    "createdAt": "2026-07-27T10:00:00Z",
    "updatedAt": "2026-07-27T10:00:00Z"
  }
}
```

Object keys, bucket names, URLs, original filenames, and file bytes are never
returned. `Idempotency-Key` is scoped to the authenticated instance; only its
SHA-256 digest is persisted.

Use `GET /campaign-media/{mediaId}` to poll metadata after an idempotent replay.
Use `DELETE /campaign-media/{mediaId}` to remove an unbound asset. An asset
referenced by a campaign returns `campaign_media_conflict`. Cross-instance
reads return `campaign_media_not_found`.

At delivery time, the worker reloads the instance-scoped asset, verifies its
immutable metadata and SHA-256 snapshot, and reads normalized bytes from the
private bucket before contacting WhatsApp. Object reads and provider media
uploads are safe, bounded retry points. The message send itself is attempted
exactly once per worker attempt. A missing acknowledgement becomes
`unknown_send_outcome`, pauses the campaign, and requires review instead of an
automatic resend.

## Lifecycle and operations

Uploads begin in `uploading` and become `ready` only after private object storage
succeeds. Validation or storage failures become `failed`; best-effort object
deletion follows. A bounded cleanup worker claims expired unbound assets with a
lease, deletes their objects, and marks them `deleted` with a fenced update.

Important error codes are:

- `invalid_campaign_media`
- `campaign_media_too_large`
- `unsupported_campaign_media_type`
- `invalid_campaign_media_dimensions`
- `campaign_media_not_found`
- `campaign_media_conflict`
- `campaign_media_storage_unavailable`

If object storage is unhealthy, disable the feature flag and investigate the
private bucket. Do not delete database rows or make the bucket public. The
additive table should remain during application rollback; cleanup can resume
after storage is restored. Pause active image campaigns before disabling the
flag. If an image target is nevertheless claimed while the provider sender is
disabled, it fails terminally with `campaign_image_content_disabled` before any
provider call.

To roll back the shared campaign cutover, disable
`WA_MEDIA_ASSETS_ENABLED` while keeping campaign image content enabled. The
legacy shadow remains readable by the previous path. Do not delete shared rows,
legacy rows, or either bucket until the later contract-migration release closes
the rollback window.
