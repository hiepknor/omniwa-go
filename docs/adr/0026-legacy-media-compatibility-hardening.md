# ADR 0026: Legacy media compatibility hardening

## Status

Accepted.

## Context

The shared media asset rollout coexists with three older paths: synchronous
`WEBHOOK_FILES` downloads, caller-supplied protobuf downloads through
`POST /message/downloadmedia`, and the legacy MinIO adapter used by rollback
shadows. Without an explicit boundary, a direct inbound image can be downloaded
twice, provider descriptor credentials can reach fan-out payloads, arbitrary
downloads can consume unbounded memory, and application startup can make an
entire legacy bucket public.

This is an L3 security and compatibility decision. It changes public error
behavior, asynchronous event payloads, object-store policy ownership, and the
rollback boundary, but does not change schema.

## Decision

When `WA_INBOUND_IMAGE_CONTENT_ENABLED` is active, the shared capture and worker
are the sole download owner for direct inbound images. Compatibility events
carry `mediaAssetId`, recursively remove credential-like image descriptor
fields, and never inline or synchronously store that direct image. The behavior
for audio, video, documents, stickers, associated-child media, and outbound
events remains unchanged. Sanitization is fail-closed even when shared capture
fails.

The legacy download endpoint remains supported but downloads through a bounded
temporary file, follows request cancellation, and uses
`REMOTE_MEDIA_MAX_BYTES` plus `MEDIA_DOWNLOAD_TIMEOUT`. It returns safe,
machine-readable errors for invalid, oversized, and timed-out media, applies an
absolute 64 MiB ceiling, and does not expose provider failures.

The legacy MinIO adapter may issue seven-day signed compatibility URLs, but it
must not log them, mutate bucket policy, or access object names outside its
validated `evolution-go-medias/` namespace. Existing bucket policies remain
operator-owned external state and are not automatically revoked.

The transactional `campaign_media_assets` shadow and routed legacy object reads
remain in place for the campaign rollback window. Their removal requires a
separate migration and evidence that all deployed readers have crossed the
shared contract boundary.

## Alternatives considered

Removing every legacy path immediately was rejected because it would break
existing webhook consumers and invalidate the campaign rollback design.
Continuing duplicate image downloads was rejected because it defeats the
asynchronous worker boundary. Automatically making the legacy bucket private
was rejected because changing external policy can break unknown consumers and
is not safely reversible by the application.

## Consequences

Legacy consumers stop receiving inline bytes or a legacy `mediaUrl` for direct
inbound images while the inbound shared feature is enabled; they receive an
opaque asset reference instead. The old download endpoint still returns a
base64 data URL but has finite resource use. Legacy signed URLs remain bearer
credentials and must be protected by consumers until that path is retired.

## Rollout and rollback

Deploy with inbound shared media disabled, verify the legacy object namespace
and inspect the existing legacy bucket policy. Enable inbound capture in
development and verify one provider download, secret-free fan-out, authenticated
content access, and unchanged non-image media events. Then stage the feature by
instance/environment.

Disable `WA_INBOUND_IMAGE_CONTENT_ENABLED` to restore the old synchronous image
webhook behavior during rollback. Bounded downloads, URL-log removal, namespace
validation, and application non-ownership of bucket policy are security fixes
and remain active. Do not remove either media table or bucket during rollback.
