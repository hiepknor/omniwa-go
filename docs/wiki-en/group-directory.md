# Group directory pagination

Use `GET /group/search` for group directory reads. It is projection-backed and
accepts `q`, `limit` (1–200), and an opaque `cursor`. Follow `meta.nextCursor`
until it is empty and inspect the projection metadata instead of assuming a
provider refresh occurred during the request.

`GET /group/myall` is deprecated because it calls the provider and returns the
entire group collection without a size bound. Responses advertise deprecation
and a 2027-02-01 sunset. Existing deployments keep it enabled by default for
compatibility; set `WA_LEGACY_GROUP_MYALL_ENABLED=false` after all callers have
migrated. The disabled endpoint returns HTTP 410 without contacting WhatsApp.
