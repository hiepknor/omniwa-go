# ADR 0025: Asynchronous inbound image assets

## Status

Accepted.

## Context

Live incoming WhatsApp image events contain provider download descriptors and
encrypted-object integrity metadata. Downloading or decoding media inside the
event callback would increase reconnect pressure and couple projection
durability to provider and object-store latency. Persisting a raw descriptor in
the projection inbox, public message model, logs, or audit trail would expose a
credential-like media key and direct path.

This is an L3 decision because it adds encrypted durable work, leases and
retries, private binary retention, a public API contract, and key lifecycle
requirements. It must remain independently reversible from outbound chat and
campaign media.

## Decision

Migration 28 links `projected_messages` to the existing instance-scoped
`media_assets` identity and changes download-job descriptors to a lifecycle
envelope. Nonterminal jobs must contain an AES-256-GCM ciphertext, 12-byte
nonce, and positive key version. Completed and failed jobs must contain none of
those fields. The authenticated additional data binds key version, instance,
message, and asset identity, preventing ciphertext substitution across scopes.

The live event callback performs one bounded database transaction: create an
inbound `downloading` asset, a time-bounded message reference, an encrypted
download job, and a secret-free audit event. Duplicate `(instance, message)`
capture returns the existing asset. It then places only `mediaAssetId` into the
sanitized projection payload. Capture failures are fail-open for message
projection and never cause raw descriptors to enter that payload.

A separate leased worker obtains the active process-local WhatsApp client. It
downloads to a size-bounded temporary file, verifies the provider plaintext
size and SHA-256, validates decoded JPEG/PNG dimensions, normalizes a canonical
rendition, and stores both immutable variants in the dedicated private shared
bucket. Claims use `FOR UPDATE SKIP LOCKED`; stale leases are reclaimable,
transient failures retry finitely with exponential backoff and stable jitter,
and known permanent failures become terminal. Database completion, projection
linking, audit, and descriptor erasure are transactional.

`GET /media-assets/{mediaId}/content` streams the canonical variant only after
instance authentication and supports one bounded byte range. It does not
return, redirect to, or sign an object-store URL. History-sync media is outside
this stage to avoid unbounded backfill work and ambiguous provider expiry.

`WA_INBOUND_IMAGE_CONTENT_ENABLED` independently gates capture, worker,
streaming route, and `chat_inbound_image_content`. It requires the shared media
foundation, private MinIO bucket, and one configured descriptor key/version.

## Alternatives considered

Synchronous download in the WhatsApp callback was rejected because provider
and storage latency would block event processing. Putting descriptors in the
projection inbox was rejected because that broadens secret access and backup
exposure. Returning MinIO or presigned URLs was rejected because authorization
would outlive the API request and become difficult to revoke. Storing image
bytes in PostgreSQL was rejected because it increases database, WAL, and backup
exposure. Automatically downloading history-sync media was deferred because it
needs a separate bounded backfill and expiry policy.

## Consequences

Message metadata can become visible before the asset is ready, so clients must
treat media status as asynchronous. A ready asset stores provider-original and
canonical bytes, increasing private object capacity requirements. The provider
descriptor key is operationally critical only while jobs are nonterminal;
terminal envelope erasure reduces long-lived secret exposure.

The initial runtime accepts one active key version. Operators must drain all
nonterminal jobs before key rotation. Multi-version key-ring loading can be
added later without changing the database envelope. Assets and message
references expire under message retention and are removed by the generic
leased cleanup worker.

## Rollout and rollback

Apply migration 28 while both media behavior flags are disabled. Verify the
foreign key, descriptor lifecycle constraint, projection schema version, and
private bucket denial of anonymous reads. Configure the descriptor key from a
secret manager, enable inbound media in development, then verify capture,
download, integrity failure, retry exhaustion, instance isolation, range
streaming, retention, and descriptor erasure before staged production rollout.

Rollback by disabling `WA_INBOUND_IMAGE_CONTENT_ENABLED`. This stops new
capture, the worker, and the content route without changing outbound or
campaign behavior. Leave migration 28 and existing rows/objects intact; schema
rollback is forward-only because removing the link or lifecycle constraint can
destroy auditability and pending encrypted work.
