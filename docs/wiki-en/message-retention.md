# Message retention

OmniWA GO persists normalized chat and message projections so list and history
reads do not query WhatsApp. Persisted message content is therefore subject to
an explicit server-side retention policy.

## Configuration

`WA_MSG_RETENTION` is a positive Go duration. Its conservative default is
`2160h` (90 days):

```env
WA_MSG_RETENTION=2160h
```

This value is an OmniWA GO data policy, not an official WhatsApp limit. Choose
it according to your legal basis, privacy notice, user expectations, backup
policy, and incident-response requirements. Shorter periods reduce exposure;
longer periods increase storage and privacy obligations. A zero or invalid
duration is rejected at startup so retention cannot be disabled accidentally.

## Deletion behavior

- New live, history-sync, and confirmed outbound messages persist a calculated
  retention deadline.
- A background worker starts with the server and deletes expired messages in
  bounded batches. It uses the current configured duration and provider
  timestamp, so shortening or extending the setting takes effect after restart
  without rewriting the whole table.
- Receipt history is hard-deleted by the database foreign-key cascade when its
  parent message is deleted.
- Normalized message and receipt payloads in the internal projection inbox are
  purged on the same cutoff, including unprocessed events that have already
  exceeded the configured policy.
- The worker logs only aggregate deletion counts and error codes. It does not
  log message content, media object keys, participant identifiers, or chat JIDs.
- Media binary data is not stored in the message projection. If external object
  storage contains media, apply a separate lifecycle policy there.

The database has a versioned retention-cutoff index. Deletion is ordered and
batch-limited to avoid a long-running table lock. Backups and replicas may keep
data beyond the primary-database deadline; operators must configure their
retention and erasure procedures separately.
