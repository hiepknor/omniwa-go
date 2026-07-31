# ADR 0050: Canonical Conversation identity configuration

- Status: Superseded in part by ADR 0051
- Date: 2026-07-31

## Context

The public product model, API, readiness capability, projection, and application
wiring use Conversation terminology. ADR 0039 removed the legacy Chat read
contract and `canonical_chat_identity` capability, but the operational setting
and Go configuration field remained named `WA_CANONICAL_CHAT_IDENTITY_ENABLED`
and `CanonicalChatIdentityEnabled`.

Environment variables are an operator-facing compatibility contract. Renaming
the setting in place would make an existing deployment silently disable
canonical reconciliation and its capabilities after upgrading. Accepting two
independent settings without conflict validation would be worse: operators could
believe the new setting wins while a rollback or manifest layer supplies the
opposite value.

Canonical identity is expected to become baseline behavior after rollout is
complete. It is not made unconditional in this change because the current flag
is still the tested emergency stop for projection reconciliation and capability
serving.

## Decision

Introduce `WA_CANONICAL_CONVERSATION_IDENTITY_ENABLED` as the canonical setting
and rename the internal Go field to `CanonicalConversationIdentityEnabled`.
Application wiring, current documentation, example configuration, and the
development Compose stack use the Conversation name.

Continue accepting `WA_CANONICAL_CHAT_IDENTITY_ENABLED` as a deprecated
compatibility alias:

- either setting alone controls the same canonical Conversation behavior;
- when both are set to equivalent boolean values, the canonical setting is used;
- when both are set to different boolean values, startup fails closed;
- any non-empty use of the deprecated alias emits a warning that contains only
  setting names, never values or secrets;
- an empty setting is treated as absent, preserving existing optional-variable
  behavior.

The default remains disabled. Contact identity reconciliation remains a required
dependency. Conversation app-state and history-sync command flags retain their
existing names and continue to require canonical Conversation identity.

No HTTP contract, capability name, projection readiness predicate, database
schema, or stored data changes.

## Alternatives

### Breakingly rename the setting

Removing the old name is superficially cleaner but can silently disable the only
public identity model in deployments whose manifests have not migrated. Rejected
until a separately approved configuration-contract cleanup.

### Keep the Chat name indefinitely

This avoids deployment work but leaves the operational model inconsistent with
the source, API, and capability model and invites new code to reuse legacy
terminology. Rejected.

### Make canonical identity unconditional now

This is the desired long-term baseline, but doing it in the same change removes
an active rollback control before every environment has completed rollout.
Deferred to a later ADR after production readiness evidence exists.

## Consequences

New deployments have one recommended Conversation-named setting. Existing
deployments remain compatible and receive an observable deprecation warning.
Conflicting layered configuration becomes an explicit startup error instead of
an ambiguous runtime state. The compatibility parser is temporary maintenance
surface and must not be reused for new flags.

Historical ADRs retain the old setting name because they describe the contract
that existed when those decisions were accepted. Current runbooks and guides use
the canonical name.

## Rollout and rollback

Deploy the compatible binary before changing environment configuration. During
the image rollback window, set both names to the same value so the new binary and
the previous binary behave identically. Then remove the deprecated name after
the rollback window closes and confirm its warning disappears.

If startup reports a conflict, correct the environment or Compose layer; do not
bypass the fail-closed check. Application rollback redeploys the previous
immutable image and restores `WA_CANONICAL_CHAT_IDENTITY_ENABLED` before startup,
because that image does not understand the new name. No database or data rollback
is required.

Removal of the deprecated alias requires a later ADR, repository-wide usage
check, and explicit operator migration notice. Graduating canonical Conversation
identity and Contact reconciliation to always-on baseline behavior is separate
follow-up work.
