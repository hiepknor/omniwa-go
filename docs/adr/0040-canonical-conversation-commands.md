# ADR 0040: Add canonical Conversation commands behind capability gates

- Status: Accepted
- Date: 2026-07-30

## Context

The public read model is now exclusively Conversation-oriented, but seven
provider mutations remain under `/chat/*`. ADR 0039 retained those operations
because their request bodies contain provider Chat identifiers. That boundary
is safe for the provider adapter but forces product clients to cross from a
canonical `conversationId` back to provider addressing metadata.

Archive, pin, and finite mute are resource-level Conversation settings when the
backend can resolve a canonical reference and an authoritative target. On-demand
history sync is also Conversation-scoped, but its provider request must use the
Chat alias, direction, timestamp, and provider message ID of a projected anchor
message. A canonical UUID is never a valid whatsmeow target.

The existing `/chat/history-sync` handler also accepted incomplete provider
metadata that could panic in service validation. Before expanding the contract,
that legacy boundary was made fail-closed and all seven legacy operations gained
bounded usage telemetry.

This is an L3 additive public-contract change. It requires staged rollout and a
rollback path, but no persistence change.

## Decision

Add these canonical operations:

- `POST /conversations/{conversationRef}/archive`
- `DELETE /conversations/{conversationRef}/archive`
- `POST /conversations/{conversationRef}/pin`
- `DELETE /conversations/{conversationRef}/pin`
- `PUT /conversations/{conversationRef}/mute`
- `DELETE /conversations/{conversationRef}/mute`
- `POST /conversations/{conversationRef}/history-sync`

Plural `/conversations/{conversationRef}` follows the existing resource
collection and makes the aggregate scope explicit. A singular
`/conversation/history-sync` action would omit the resource identity from the
path, diverge from the read contract, and encourage provider metadata in the
body. History sync therefore belongs below one resolved Conversation resource.

Both canonical and absorbed references use the existing instance-scoped
resolver. Responses always contain the current canonical `conversationId`.
Archive, pin, and mute resolve the authoritative `addressingJid` and support
direct and group Conversations only. Newsletter, broadcast, and unknown types
fail closed with `unsupported_conversation_operation` until their provider
semantics are proven.

Mute accepts a finite duration from 60 seconds through 365 days. Infinite mute
is deliberately excluded because the canonical projection currently represents
only `mutedUntil`; exposing an unrepresentable state would make the response
non-authoritative.

History sync accepts only `anchorMessageId` and a count from 1 through 1000.
The projected anchor must belong to the resolved Conversation and the current
instance. The application derives its provider Chat alias, direction, group
status, and provider timestamp. This is important for direct Conversations
whose messages may span PN and LID aliases: `addressingJid` is not substituted
for the alias on which the anchor exists.

Provider acknowledgement returns HTTP 202. The command does not optimistically
mutate the projection. Archive, pin, and finite mute app-state events are
normalized and projected as exact field aspects, so one event cannot overwrite
unrelated Conversation settings. Canonical summaries select the newest version
of each setting across PN/LID aliases instead of selecting all settings from the
alias with the newest message activity.

The canonical operations share the legacy provider service boundary; business
rules and identity resolution exist only in the canonical application service.
The `/chat/*` routes remain compatibility adapters during migration.

Add two independently deployable, instance-scoped capabilities:

- `conversation_app_state_commands`
- `conversation_history_sync`

Each has a corresponding default-off configuration flag. A capability is
advertised only when its route is enabled and the same resources and durable
readiness predicate as `canonical_conversation_identity` pass. No capability is
derived from a version string. Enabling either flag while canonical identity is
disabled is a configuration error.

## Alternatives considered

### Keep only provider Chat commands

This has the smallest backend surface, but every product client must retain
provider targeting logic and can confuse `conversationId`, `addressingJid`, and
an anchor message's provider alias. It also prevents the public API from
expressing its actual aggregate. Rejected as the long-term model.

### Rename `/chat/*` in place

This is smaller after deployment but breaks existing clients and cannot safely
reinterpret a provider Chat request body as a canonical reference. Rollback is
also harder because clients may immediately adopt the renamed operation.
Rejected.

### Add canonical commands, then retire compatibility with evidence

This temporarily duplicates transport routes but shares provider execution and
keeps semantics explicit. Capability flags, parity tests, and bounded telemetry
support mixed-version rollout. Selected.

## Consequences and risks

- Product clients no longer need to send provider addressing metadata for these
  operations.
- A provider acknowledgement can be followed by a delayed or missing app-state
  event. HTTP 202 communicates that gap; monitoring must compare command errors
  and projection freshness.
- Newsletter and broadcast support is deferred rather than guessed.
- Finite mute is less feature-complete than the provider adapter but remains
  representable and authoritative.
- Seven temporary compatibility routes increase OpenAPI surface. Shared service
  methods and contract tests limit drift.
- No database or data migration is required. Existing JSON field-version maps
  can record the new exact aspects.

## Rollout and rollback

Deploy with both new flags disabled. Enable the app-state and history-sync
capabilities independently on development only after canonical Conversation
readiness is present. Verify canonical/absorbed reference parity, PN/LID anchor
selection, cross-instance and cross-Conversation rejection, provider failures,
projection updates, and capability removal when projection readiness fails.

Clients switch per capability, not per version. Monitor bounded canonical and
`provider_chat` operation/status-class metrics. Do not remove `/chat/*` until
all supported consumers have cut over and successful authenticated legacy
traffic remains at zero for the agreed observation window.

Rollback first disables the new flags and restarts the immutable image. The
legacy provider routes continue to work. If event normalization regresses,
redeploy the previous image; no database rollback is needed. Re-enable or retain
legacy routes if any supported consumer is discovered during observation.
