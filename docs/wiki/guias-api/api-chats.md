# Conversation API

The public product resource is a canonical Conversation. Provider Chat IDs and
JIDs are accepted only as absorbed lookup aliases or used internally as command
targets; they are not public entity identities.

## Read operations

- `GET /conversations`
- `GET /conversations/{conversationRef}`
- `GET /conversations/{conversationRef}/messages`
- `GET /conversations/{conversationRef}/messages/{messageId}`

`conversationRef` accepts the canonical UUID or a current/absorbed provider
Chat alias. Every successful response normalizes identity to `conversationId`.
Cursors are opaque and scoped to the resolved canonical Conversation.

## App-state commands

These operations require the `conversation_app_state_commands` capability:

- `POST /conversations/{conversationRef}/archive`
- `DELETE /conversations/{conversationRef}/archive`
- `POST /conversations/{conversationRef}/pin`
- `DELETE /conversations/{conversationRef}/pin`
- `PUT /conversations/{conversationRef}/mute`
- `DELETE /conversations/{conversationRef}/mute`

A finite mute request uses this body:

```json
{
  "durationSeconds": 3600
}
```

## History sync

`POST /conversations/{conversationRef}/history-sync` requires the
`conversation_history_sync` capability. The projected anchor must belong to
the resolved Conversation:

```json
{
  "anchorMessageId": "3EB0C5A277F7F9B6C599",
  "count": 50
}
```

Commands return HTTP 202 after provider acknowledgement. They do not imply that
the projected state has already converged. Direct and group Conversations are
supported; unsupported Conversation types fail closed with
`unsupported_conversation_operation`.

## Removed provider Chat commands

The former `/chat/archive`, `/chat/unarchive`, `/chat/mute`, `/chat/unmute`,
`/chat/pin`, `/chat/unpin`, and `/chat/history-sync` operations have been
removed. They are not compatibility aliases for the canonical endpoints.
