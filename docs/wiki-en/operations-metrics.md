# Prometheus metrics

OmniWA GO exposes process and bounded application metrics at `GET /metrics`.
The endpoint requires the global admin key; instance tokens are rejected.

```bash
curl -H "apikey: $GLOBAL_API_KEY" http://localhost:4000/metrics
```

Configure the scraper or a trusted metrics proxy to send the header using the
deployment's secret-management mechanism. Never place the key in a checked-in
scrape file.

The registry includes standard Go/process collectors. Group List eligibility
metrics use only bounded operation, eligibility-state, and public rejection-code
labels. They never contain instance IDs, WhatsApp JIDs, participant data,
authorization evidence, credentials, or raw provider errors.

Runtime-role metrics use only the fixed roles `starting`, `standby`,
`promotion_pending`, `active`, `draining`, and `terminated`:

- `omniwa_runtime_role{role}` is a one-hot gauge for the latest process role.
- `omniwa_runtime_ready` is `1` only while the process role is `active`.
- `omniwa_runtime_role_transitions_total{from,to}` counts successful bounded
  role transitions.

The unauthenticated `GET /server/live` endpoint reports process liveness, while
`GET /server/ready` returns HTTP 200 only for the active role and HTTP 503 for
all other roles. Both responses are non-cacheable and intentionally omit role,
dependency, and topology details. Existing deployments must keep using
`/server/ok` until the new endpoints have been verified; changing a proxy or
orchestrator health check is a separate rollout step.

Configured infrastructure is checked asynchronously and exposed through the
authenticated `GET /server/health` response and the one-hot
`omniwa_dependency_health{dependency,status}` gauge. Allowed statuses are
`unknown`, `healthy`, and `unavailable`; allowed dependencies are
`users_database`, `external_event_outbox`, `rabbitmq`, `legacy_media`,
`media_assets`, and `campaign_media`. These observations are cached and never
put a downstream network call on the HTTP request path. They intentionally do
not change `/server/ready` in this rollout. Raw errors, endpoints, bucket names,
instance identifiers, and credentials are not exposed.
`omniwa_dependency_last_check_timestamp_seconds{dependency}` allows alerting
when a probe stops updating even if its last cached state was healthy.

Metrics are process-local and reset when the application restarts. Use the
persisted server overview and projection-health endpoints for durable state and
readiness diagnostics.
