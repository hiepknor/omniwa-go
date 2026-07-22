# ADR 0020: Immutable artifact build and promotion

## Status

Implemented

## Context

Every push to `main` currently builds and pushes the same semantic-version and
`latest` tags. Independent workflow runs may finish out of order, allowing an
older commit to overwrite a newer mutable tag. A development redeploy already
observed an image revision behind repository `main`.

Cancelling older workflows reduces but does not eliminate this race once an
older job has begun publishing.

## Decision

Build each commit once and publish an immutable `sha-<commit>` tag plus its
digest. CI, integration tests, container smoke tests, SBOM generation, and
provenance complete before promotion. A separate promotion step accepts an
existing digest, verifies its source revision against the expected `main` HEAD
or Git release tag, and attaches release aliases without rebuilding.

Semantic version aliases are created only from Git releases. An existing alias
may be reused only when it already resolves to the same digest; promotion fails
instead of moving it. Deployments record and use image digests. No `latest`
application alias is published or accepted by the maintained deployment files.

The application reports its source revision through non-sensitive capability
metadata, and the OCI revision label uses the same value. Deployment
verification requires:

```text
expected source SHA = OCI revision = reported runtime revision
```

Artifact verification reads the immutable registry index, selects exactly one
`linux/amd64` platform manifest, and validates that manifest's config. It does
not rely on `docker image inspect` resolving an OCI index in a local image
store, because that behavior differs between Docker storage backends.

Versioned database migrations remain immutable. Upgrade smoke tests cover an
empty database and the previous supported release. Checksum mismatches stop the
deployment; automation never repairs them silently.

## Consequences

- Out-of-order builds cannot roll a deployment backward.
- Promotion becomes explicit and auditable.
- Storage contains more immutable tags, requiring a retention policy.
- Emergency rollback selects a known-good digest instead of rebuilding source.

## Rollout and rollback

Add SHA tags before changing deploy consumers. Move dev and staging to digests,
then production. The previous known-good digest is the application rollback;
additive database migrations use forward-fix strategy.
