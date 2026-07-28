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

Metrics are process-local and reset when the application restarts. Use the
persisted server overview and projection-health endpoints for durable state and
readiness diagnostics.
