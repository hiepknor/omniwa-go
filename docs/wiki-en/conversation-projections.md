# Conversation projections

Conversation list reads are served from instance-scoped PostgreSQL projections.
They never call WhatsApp, and chat list reads do not perform per-row contact
lookups.

## Canonical contacts

`ContactInfo` preserves the existing Pascal-cased fields and adds:

- `contactId` (required): stable opaque UUID within the authenticated instance.
- `addressingJid` (required): phone JID when known, otherwise LID, otherwise the
  persisted preferred JID. Use this value for commands.
- `aliases` (required): deduplicated known active JID, phone-JID, LID, and
  username alias values. Clients should treat the array as lookup material, not
  additional contacts.
- `identityStatus` (required): `complete` when phone and LID are both known,
  otherwise `partial`.
- `identityUpdatedAt` (required): last canonical projection update.
- `displayName` and `displayNameSource` (optional): present only when a real
  projected name is known.

Contacts merge only on authoritative aliases supplied by normalized events or
the local whatsmeow LID mapping store. Names never merge contacts. IDs are
instance scoped, and an absorbed ID permanently redirects to its canonical
contact. `GET /user/contact/{contactId}` accepts a current UUID, an absorbed
UUID, or a contact JID alias and always returns the canonical ID.

Search applies Unicode NFKC normalization, collapses whitespace, and compares
case-insensitively across canonical/absorbed IDs, aliases, username, and names.
Pagination is over canonical contacts. Version-2 cursors are opaque and bound
to the instance and normalized query; old or mismatched cursors return HTTP 400
with `invalid_cursor`.

## Chat names

Direct chats expose canonical `contactId` when the mapping is known and copy the
contact name into `displayName`. Precedence is `FullName`, `BusinessName`,
`PushName`, `FirstName`, then `Username`; `displayNameSource` reports the chosen
source and `displayNameUpdatedAt` reports its projection timestamp. A contact
rename updates linked direct chats transactionally.

Group, newsletter, and broadcast names use `group_subject`, `newsletter_name`,
and `broadcast_name` respectively. Direct provider names use `provider_chat`
only while no canonical name is available. A chat remains present when contact
identity or name is unknown. The backend never synthesizes a display name from
a phone number or other sensitive identifier.

## Totals and readiness

`GET /chat/list` returns the exact active projected-chat count in `meta.total`.
`GET /user/contacts` returns the exact active canonical-contact count, and
`GET /user/contacts/search` returns the exact canonical count matching the
normalized query. Totals are recomputed for each request and remain stable
across pages when the dataset is unchanged; they are not snapshot-isolated from
concurrent writes. Search does not provide `unfilteredTotal`.

A ready empty projection returns `total: 0`. A projection that is not started,
failed, or has never completed reconciliation returns `projection_not_ready`
instead of a false empty result. A previously reconciled syncing or stale
projection may be served with its explicit `syncStatus`.

The `canonical_contact_identity` capability appears only when Contacts and
Chats are ready at their current schema versions and the instance's local LID
reconciliation checkpoint is complete. Clients must not infer canonical
identity support from `contacts_projection` alone.
