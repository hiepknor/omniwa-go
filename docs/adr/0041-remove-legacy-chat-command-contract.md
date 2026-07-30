# ADR 0041: Remove the legacy provider Chat command contract

- Status: Accepted
- Date: 2026-07-30
- Supersedes: the temporary compatibility boundary in ADR 0040

## Context

ADR 0040 added canonical Conversation commands while retaining seven provider
Chat command routes for compatibility. The canonical application service now
owns reference resolution, projection readiness, supported-type checks, and
the translation from a canonical Conversation to the WhatsApp addressing JID
or projected anchor alias. The legacy handlers only accepted caller-supplied
provider metadata and delegated to the same provider adapter.

The owned Console consumer cut over to canonical Conversation reads in
`omniwa-console` PR 105 and its follow-up PR 106. The reviewed Console source
and deployed bundle contain no executable `/chat/*` call site. The development
deployment advertised both canonical command capabilities, and the legacy
command metric had no successful request after cutover; its only current value
was a deliberately invalid history-sync smoke request.

This evidence cannot prove that an unregistered external client does not exist,
and the elapsed observation window is shorter than the conservative rollout in
ADR 0040. The project owner explicitly accepted that residual compatibility
risk and directed immediate physical removal.

## Decision

Remove these public operations:

- `POST /chat/archive`
- `POST /chat/unarchive`
- `POST /chat/mute`
- `POST /chat/unmute`
- `POST /chat/pin`
- `POST /chat/unpin`
- `POST /chat/history-sync`

Remove their route registrations, transport handlers, provider-request DTOs,
legacy wrapper service methods, generated OpenAPI operations and schemas, and
the `provider_chat` request-metric contract label. A request to a removed route
receives the router's normal not-found response; no tombstone or fallback is
added.

Keep the canonical operations under `/conversations/{conversationRef}` and the
independent `conversation_app_state_commands` and `conversation_history_sync`
capabilities. Their readiness predicates and default-off configuration flags do
not change.

Keep the internal `ConversationCommandProvider` boundary and genuine WhatsApp
Chat/JID terminology. Archive, pin, and mute still execute against the resolved
`addressingJid`; history sync still executes against the provider Chat alias of
the authoritative projected anchor message. Provider provenance, persistence
columns, PN/LID aliases, whatsmeow types, and published migrations are not
renamed or removed.

This is an API/application-layer contraction only. It requires no schema or
data migration.

## Alternatives

### Complete a longer observation window

This minimizes compatibility risk for observable consumers but delays removal
after the only owned consumer has already cut over. It was the preferred
operational plan in ADR 0040 and was rejected here by explicit owner direction.

### Keep not-found tombstones or deprecated OpenAPI operations

This could provide a more descriptive error but would preserve dead public
surface and generated-client methods. It was rejected in favor of physical
contract removal.

### Remove provider terminology from the implementation

This would conflate canonical entity identity with WhatsApp command targeting
and increase upstream-sync conflicts. It was rejected; provider terminology
remains correct below the application boundary.

## Consequences

- Product clients have one Conversation command contract and cannot submit a
  provider JID as public entity identity.
- Existing callers of any removed route break immediately and must resolve a
  canonical or absorbed `conversationRef` first.
- Historical `contract="provider_chat"` Prometheus series may remain in durable
  storage, but the new binary cannot emit new samples.
- The public OpenAPI contract becomes smaller while provider execution code is
  still shared by every canonical command.
- Newsletter, broadcast, unknown, and infinite-mute semantics remain fail-closed
  and are not expanded by this removal.

## Rollout and rollback

Regenerate OpenAPI, run the complete build, vet, unit, integration, race, and
contract gates, then deploy the immutable image to development. Verify all
seven `/chat/*` operations are absent from routing and OpenAPI while the seven
canonical operations remain capability-gated and functional for ready
instances.

Rollback redeploys
`ghcr.io/hiepknor/omniwa-go@sha256:cdc64f2017f836e430fe6c34f013c0f751209cf2ee8693a4ba0dd1fbe4c618f4`,
revision `c9db9be5408afe0493f8c14e93500b47c7936990`. No database rollback is
required. Restore that image if a supported legacy consumer is discovered or a
canonical command regression cannot be forward-fixed safely.
