# ADR 0015: Model and module boundaries

## Status

Accepted

## Context

OmniWA GO is a downstream fork and must preserve its package and module paths.
At the same time, several existing structs currently act as persistence records,
domain objects, provider payloads, and public API responses. This makes secrets
easy to serialize, couples handlers to storage details, and makes changes hard
to test independently.

A repository-wide package move would create high upstream-sync cost. A layered
framework with generic `entity`, `usecase`, and `adapter` packages would also be
unidiomatic for this codebase.

## Decision

Keep a single Go module and the existing package-per-domain modular monolith.
Do not move or rename existing top-level packages. Improve boundaries
incrementally in the domain being changed:

- Persistence records may carry GORM tags but must not be serialized directly
  by handlers.
- Public request and response DTOs live at the HTTP boundary and explicitly
  select every exposed field.
- Domain/service types do not depend on Gin or HTTP status codes.
- Repository code does not produce public HTTP responses.
- Provider-native whatsmeow and protobuf values are normalized before they are
  persisted or exposed publicly.
- Projection records remain read-optimized models and are mapped to public
  views by readers or services.
- Interfaces are owned by the consuming package when practical.
- Shared cross-cutting behavior receives a named package and contract; it must
  not accumulate in a generic `common` package.

New application wiring may be extracted into `pkg/bootstrap`, while
`cmd/evolution-go` remains the entrypoint. Runtime ownership initially remains
inside the existing whatsmeow/service boundary to avoid moving provider code.

## Dependency direction

The required direction is:

```text
handler -> service -> consumer-owned repository/provider interface
                         -> concrete database/provider adapter
```

Imports in the reverse direction are not allowed. Sensitive persistence
records must use `json:"-"` or a private record type even when a compatibility
DTO temporarily continues to expose a credential.

## Adoption

This decision is applied by vertical slice. Existing domains are not rewritten
solely for conformity. The instance domain is first because its persistence
model currently contains an authentication token. Runtime ownership, shared
HTTP errors, and bootstrap extraction follow as separate changes.

## Consequences

- Upstream sync remains localized because package paths do not change.
- Public contracts stop depending accidentally on database schemas.
- Some temporary mapping code is required during migration.
- Legacy models may coexist with public DTOs until compatibility consumers have
  migrated.

## Rollback or replacement

Individual DTO mappings can be rolled back without schema changes. Replacing
the modular-monolith decision requires a separate ADR and explicit evidence
that independent deployment is worth the operational and sync cost.

The first adoption slice introduces an explicit instance compatibility view for
create, list, and info responses. Every persistence field is marked
`json:"-"`, and handlers map the selected public fields instead of serializing
the GORM record. The existing response keys remain stable, including the token
during the credential migration window, while stored proxy credentials and QR
ceremony material are always redacted. Internal handlers obtain the authenticated
instance by type assertion rather than JSON round-tripping the persistence
record.

The bootstrap adoption slice introduces `pkg/bootstrap` without moving the
entrypoint or provider code. A process supervisor now owns background worker
registration, cancellation context propagation, error reporting, shutdown
sealing, and completion signaling. This prevents `WaitGroup.Add` from racing
with shutdown `Wait` and removes per-worker goroutine bookkeeping from
`main.go`. The package also constructs the typed instance runtime registry;
runtime lifecycle behavior remains in `pkg/instance/runtime` and the WhatsApp
adapter. Domain workers still own their processing logic and accept context,
so bootstrap remains composition rather than a generic business layer.

Architecture boundaries are enforced by AST-based tests in
`pkg/architecture`. They reject reverse layer imports, direct construction or
use of process-global HTTP clients outside `pkg/netguard`, serializable
sensitive fields in persistence model packages, and raw maps of WhatsApp
runtime clients. The checks inspect production Go syntax rather than matching
comments or documentation, understand aliased `net/http` imports, and exclude
the protected `pkg/core` implementation. New exceptions require an explicit
architecture decision instead of silently growing an allowlist.
