# ADR 0043: Use file-backed secrets in production deployments

- Status: Accepted
- Date: 2026-07-30

## Context

The application read sensitive configuration only from environment variables.
The production Compose examples also embedded predictable PostgreSQL,
RabbitMQ, MinIO, and API credentials, while the Swarm reference embedded the
global API key and complete database DSNs. Environment variables are useful for
native development, but process inspection, generated Compose output, support
bundles, and accidental configuration commits make them a poor default secret
transport.

The production examples additionally used mutable dependency image tags and
published database, broker, and object-storage ports on every host interface.
Those defaults conflict with the repository's immutable application-image
release model and expose infrastructure that only the application network
needs.

This is an L3 security and operational compatibility decision. It changes
deployment defaults but does not change application API contracts or stored
data.

## Decision

Sensitive application settings accept either their existing environment
variable or a matching `NAME_FILE` variable. File-backed values are bounded to
one MiB, reject NUL bytes, remove only trailing line endings, and fail startup
when both a non-empty direct value and file path are configured. Existing
direct variables remain supported for native development and staged rollback.

The supported file-backed settings are:

- PostgreSQL authentication and users DSNs, and component password
- global API key
- instance-token HMAC key and media-descriptor key
- AMQP and NATS URLs
- webhook URL
- audio-converter API key
- proxy password
- MinIO access and secret keys

Production Compose mounts the global API key, database DSNs, and PostgreSQL
password as service-scoped secrets. The full override additionally mounts the
AMQP URL, RabbitMQ configuration, and MinIO root credentials. RabbitMQ receives
a secret configuration file because current official RabbitMQ images no longer
support the former Docker-specific password-file variables. MinIO uses its
documented root credential file variables. Swarm references pre-created
external secrets and passes only their mount paths to the application.

Production dependency images must be supplied as immutable digest references.
PostgreSQL is no longer published on the host. RabbitMQ and MinIO administrative
ports bind to loopback only; containers continue communicating over the private
Compose network. Development and CI stacks retain explicit disposable fixture
credentials and their current accessibility.

Secret source directories are ignored by Git, generated OpenAPI is unaffected,
and CI validates the base Compose, full Compose, and Swarm renderings with
ephemeral fixtures.

## Alternatives

### Keep direct environment variables and replace only literal defaults

This prevents committed example credentials but leaves secrets visible in
container environment metadata and encourages the same operational pattern.
It was rejected as the production default, while retained as a compatibility
path.

### Add provider-specific secret handling only in manifests

PostgreSQL and MinIO support file variables, but the application and RabbitMQ
interfaces differ. Provider-only handling would leave database DSNs and API
credentials in the application environment and create inconsistent operator
semantics. A common application `NAME_FILE` convention was selected.

### Depend on an external secret-manager SDK in application code

This can support dynamic rotation but couples the binary to a specific control
plane and does not solve local Compose or Swarm uniformly. Mounted files are a
portable boundary; an external controller may materialize and rotate them.

### Keep mutable dependency tags for automatic patches

This reduces manual image maintenance but makes deployment output
non-reproducible and rollback uncertain. Explicit digest promotion was selected
to match the application image policy.

## Consequences

- Existing native and legacy deployments continue working with direct values.
- Production manifest users must materialize secret files or external Swarm
  secrets before rendering the stack.
- Migrating an old Compose `.env` requires removing non-empty direct values
  after the corresponding secret files are mounted; ambiguous dual sources
  intentionally fail startup.
- File mounts reduce secret exposure but do not provide rotation by themselves.
  Rotation requires replacing the secret and recreating the service.
- Local Compose secret files are protected by host filesystem permissions, not
  an encrypted secret store. Swarm secrets use the platform secret lifecycle.
- Dependency upgrades become explicit operational work rather than an implicit
  effect of pulling a mutable tag.
- Removing host publication may affect external database tooling; operators use
  `docker compose exec`, an SSH tunnel, or an explicit local override.

## Rollout and rollback

First deploy the new binary with the existing direct environment configuration
and verify unchanged startup. Materialize restricted secret files, render the
manifests, then switch one development deployment to `NAME_FILE` sources and
verify restart survival without direct secret variables. Promote the same
configuration after checking authenticated API, database, RabbitMQ, and MinIO
connectivity.

Rollback switches the application back to the direct variable and removes its
matching `NAME_FILE` value before recreating the service. A manifest rollback
may restore prior port publishing only when operationally necessary; it must
not restore checked-in or predictable credentials. No database rollback is
required.
