# ADR 0033: Unified Conversations media contract

## Status

Accepted.

## Context

Outbound device images and asynchronous inbound images already share private,
instance-scoped media assets, but they were advertised as separate product
capabilities and several lifecycle failures collapsed into `not_ready` or
`not_found`. Projected-message requiredness also lagged runtime validation.

## Decision

`conversation_media_assets` is advertised only when outbound device image
content and inbound image content are configured and the current Messages
projection is ready. Existing media capabilities remain for compatibility;
`group_photo_assets` is not reused.

Upload remains authenticated, JPEG/PNG-only, bounded, and idempotent within an
instance. Metadata reports every persisted lifecycle state, including deleted,
without storage coordinates. Tenant lookups remain SQL-scoped, so a foreign
asset UUID appears not found rather than disclosing another tenant's asset.

Content and send distinguish failed, expired, deleted, not-ready, integrity,
and storage failures. Content is size- and SHA-256-verified into a bounded
temporary file before response bytes. Sending fences retention, verifies the
object, makes exactly one provider attempt, and returns additive `messageId`
and `timestamp` acknowledgement fields. They are not delivery receipts.

Projected messages require `messageId`, `chatId`, `direction`, `messageType`,
`providerTimestamp`, and `provenance`. Content, participant, media, receipt,
history-sync, and retention fields remain optional. An asset failure never
removes its projected message.

Cleanup remains reference-fenced and may proceed only when no live reference,
active download job, or compatibility shadow remains. This decision adds no
new automatic deletion strategy.

## Consequences

Content reads add bounded temporary disk I/O and storage latency to detect an
integrity failure before a successful response starts. Existing routes, legacy
send branches, response fields, and capabilities remain available.

## Rollout and rollback

Publish regenerated OpenAPI with the runtime. Enable both media flags only
after the private bucket, descriptor key, Messages projection, and cleanup
worker are healthy. Roll back by disabling either media flag while retaining
the additive schema, assets, references, and private bucket.
