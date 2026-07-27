# ADR 0023: Durable campaign media assets

## Status

Accepted for staged implementation. The media foundation is inert unless
`WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=true`; image campaign creation and delivery
remain unavailable until later stages advertise `campaign_image_content`.

## Context

Campaigns currently persist text inline and execute recipient work through
durable leases, bounded retry, instance circuits, and an explicit unknown-send
outcome. The interactive `/send/media` endpoint can accept multipart images,
but its request-lifetime byte buffers, hidden provider retries, and URL-oriented
storage contract are not safe for scheduled or restartable campaign work.

An uploaded image may need to survive process restarts and remain unchanged for
the complete campaign lifetime. Uploading an object and committing PostgreSQL
cannot be one atomic transaction. Object names, signed URLs, authorization
material, and user filenames must not become cross-instance identifiers or
audit payloads. A storage outage must not turn into an ambiguous provider send.

This is an L3 change because it adds persistent state, a public upload API, an
external storage dependency, cleanup concurrency, and a future provider
boundary. The first stage deliberately does not alter campaign content or send
behavior.

## Decision

### Two-step contract

Clients upload one image first, receive an opaque media asset UUID, and later
reference that UUID from a JSON campaign request. Campaign creation never
combines a multipart body with Group List snapshotting.

The foundation exposes instance-authenticated routes:

```text
POST   /campaign-media
GET    /campaign-media/{mediaId}
DELETE /campaign-media/{mediaId}
```

Only an unreferenced asset may be deleted. Image campaign references are added
in a later migration and API stage.

### Durable asset state

`campaign_media_assets` owns immutable, instance-scoped metadata:

- UUID and instance UUID;
- server-generated private object key;
- media type, detected MIME type, bounded byte size, width, and height;
- SHA-256 digest;
- `uploading`, `ready`, `failed`, or `deleted` state;
- created, updated, ready, expiry, and deletion timestamps.

The unique `(id, instance_id)` identity supports an instance-scoped campaign
foreign key in the next stage. Ready metadata and object keys cannot be changed
through the public service. Object keys contain only server-generated UUIDs,
never user filenames.

### Private object storage

Campaign media uses a narrow streaming object-store interface and a dedicated
S3-compatible bucket configured by `CAMPAIGN_MEDIA_BUCKET`. It does not reuse
the legacy public-URL media interface. The implementation never changes bucket
policy, returns signed URLs, or logs object URLs or payloads.

The upload path writes a bounded temporary file, detects the image from bytes,
checks dimensions and pixel count, decodes and re-encodes JPEG or PNG to remove
untrusted metadata, calculates SHA-256, then streams the normalized file to the
private bucket. The default limits are 8 MiB and 16 megapixels.

### Failure and cleanup model

The repository creates an `uploading` row before object I/O. A successful put
atomically changes that row to `ready`. A failed put changes it to `failed` and
attempts best-effort object deletion. A bounded cleanup worker deletes objects
for expired unreferenced assets and records the terminal database state.

Cleanup claims rows with PostgreSQL row locks and `SKIP LOCKED`. It never
deletes an asset referenced by a campaign once that foreign key is introduced.
Until then, only the configured unbound TTL is relevant.

### Security and isolation

- Every repository lookup includes `instance_id`.
- Multipart requests are bounded independently of the legacy media endpoint.
- MIME type comes from decoded bytes, not request headers or filenames.
- Width, height, decoded pixel count, normalized size, and caption limits are
  backend policy.
- Responses and audit metadata exclude object keys, user filenames, signed
  URLs, and file bytes.
- An idempotency key is instance-scoped and stores only a SHA-256 hash.

### Capability and rollout

`WA_CAMPAIGN_IMAGE_CONTENT_ENABLED` defaults to `false`. In this foundation
stage it controls route and storage initialization but does not advertise a new
capability. `campaign_image_content` is advertised only after schema, campaign
contract, storage, worker sender, safety behavior, and documentation are all
available.

Rollout order:

1. Apply the additive asset migration while the flag is false.
2. Provision and verify the private campaign bucket.
3. Enable uploads in a canary environment after this foundation deploys.
4. Deploy the content-contract and delivery stages while their send capability
   remains disabled.
5. Advertise and enable image campaigns only after end-to-end verification.

## Alternatives considered

### Multipart campaign creation

Rejected because database snapshotting and object upload cannot commit
atomically, HTTP retries would duplicate large uploads, and the campaign API
would have two incompatible body formats.

### Base64 in PostgreSQL or campaign JSON

Rejected because it expands request and database size, increases memory use,
complicates retention, and makes every campaign read carry binary data.

### Persist a presigned URL

Rejected because URLs expire, may contain authorization query material, and do
not provide an immutable server-owned campaign reference.

### Reuse the legacy media storage and `/send/media` sender directly

Rejected because the current contract is byte-slice and public-URL oriented,
and the sender owns hidden retry beyond the campaign worker's safety boundary.

### Browser-to-S3 presigned upload

Deferred. It reduces application bandwidth but requires CORS, upload-finalize
proof, abandoned multipart cleanup, and client-visible object-store behavior.
The bounded backend-streaming endpoint is a smaller secure first contract.

## Consequences

The solution adds a PostgreSQL row and private object for every uploaded image,
plus cleanup and storage health responsibilities. It provides an immutable,
restart-safe artifact and keeps binary I/O outside campaign transactions. The
future sender can distinguish object-read and provider-upload failures from the
unknown message-send boundary.

Image delivery may initially upload normalized bytes to WhatsApp per target.
Provider-upload reuse is an optimization only after correctness metrics prove
the need; encrypted provider descriptors are not persisted in this decision.

## Rollback and recovery

Set `WA_CAMPAIGN_IMAGE_CONTENT_ENABLED=false` before rolling back the
application. This removes upload routes and prevents later stages from creating
new image campaigns. Keep the additive table and private objects in place.
Repair schema defects with a forward migration.

If object storage is unavailable, leave ready metadata unchanged, reject new
uploads with `503 media_storage_unavailable`, and defer future delivery before
the provider boundary. Stale `uploading`, `failed`, or expired unreferenced
assets are recoverable through the bounded cleanup worker. No rollback step may
make the campaign bucket public or delete referenced objects.
