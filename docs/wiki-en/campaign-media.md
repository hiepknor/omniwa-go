# Campaign media assets

Campaign media assets are immutable, instance-scoped images uploaded from a
device before an image campaign is created. The upload foundation is separate
from campaign delivery: enabling these routes alone does **not** mean the server
can create or send image campaigns, and it does not advertise
`campaign_image_content`.

## Deployment prerequisites

The feature requires PostgreSQL migration 24 and a dedicated private
S3-compatible bucket. The application verifies that the bucket exists at
startup but never creates it or changes its access policy.

```env
MINIO_ENABLED=true
CAMPAIGN_MEDIA_BUCKET=omniwa-campaign-media
CAMPAIGN_MEDIA_MAX_BYTES=8388608
CAMPAIGN_MEDIA_MAX_PIXELS=16000000
CAMPAIGN_MEDIA_UNBOUND_TTL=24h
WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=true
```

Provision the bucket as private before enabling the flag. Do not reuse a bucket
whose objects are publicly readable. Keep the flag disabled during migration
and rollback. When the flag is disabled, `/campaign-media` routes are not
registered and the cleanup worker is not started.

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
Use `DELETE /campaign-media/{mediaId}` to remove an unbound asset. Cross-instance
reads return `campaign_media_not_found`.

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
after storage is restored.
