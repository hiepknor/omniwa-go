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

Metrics are process-local and reset when the application restarts. Use the
persisted server overview and projection-health endpoints for durable state and
readiness diagnostics.
