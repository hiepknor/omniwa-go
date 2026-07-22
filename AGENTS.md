# AGENTS.md

Guidance for AI coding agents working on OmniWA GO. Read this before making
changes. For human contributor details see [CONTRIBUTING.md](./CONTRIBUTING.md).

## Project overview

OmniWA GO is a WhatsApp API written in Go on top of the
[whatsmeow](https://github.com/tulir/whatsmeow) library. It is a **fork of**
[Evolution Go](https://github.com/evolution-foundation/evolution-go) that tracks
and merges upstream releases (see [docs/SYNC.md](./docs/SYNC.md)).

Layout:

- `cmd/evolution-go/` — application entrypoint (`main.go`).
- `pkg/<domain>/` — one package per domain (`instance`, `message`, `sendMessage`,
  `chat`, `group`, `call`, `community`, `label`, `newsletter`, `poll`, `user`),
  each split into `handler/`, `service/`, and (where persisted) `repository/`.
- `pkg/events/` — event producers: RabbitMQ, NATS, Webhook, WebSocket.
- `pkg/storage/` — media storage (MinIO/S3).
- `pkg/middleware/` — auth (`apikey` header) and JID validation.
- `pkg/config/` — env-driven configuration.
- `pkg/core/` — license gate + runtime (obfuscated; see hard rules below).
- `manager/dist/` — prebuilt React console UI (do not modify; see hard rules).
- `docker/` — compose files (`docker/README.md`).

Data stores: PostgreSQL (GORM "users" DB + an auth DB for whatsmeow sessions).
RabbitMQ, NATS, and MinIO are optional and disabled unless configured.

## Setup, build, test, run

```bash
go build ./...      # build everything
go vet ./...        # static checks
go test ./...       # run tests
make fmt            # gofmt (there is no golangci-lint config; rely on vet + gofmt)
make swagger        # regenerate docs/ after changing handlers or the @title
```

Always run `go build ./... && go vet ./... && go test ./...` before opening a PR;
they must pass (CI enforces `build / vet / test`).

Run the app:

```bash
# Native (needs a .env — copy .env.example first)
make dev

# Full dev stack (Postgres + RabbitMQ + MinIO, license gate OFF, port 4000)
cd docker && docker compose -f docker-compose.dev.yml up -d
curl http://localhost:4000/server/ok      # unauthenticated liveness
```

Authenticated calls use the `apikey` header. Instance-scoped routes accept an
instance token; admin routes require `GLOBAL_API_KEY`.

## ⚠️ Hard rules (do NOT break these)

These protect the fork's ability to merge upstream cleanly and its license
compliance. They are easy to violate by "fixing" things — don't.

- **Do NOT change the Go module path** `github.com/evolution-foundation/evolution-go`.
  The repo is named omniwa-go but the module path is intentionally kept. Changing
  it rewrites ~165 imports and causes conflicts on every upstream sync.
- **Do NOT rename or move `cmd/evolution-go/`.**
- **Do NOT restructure `pkg/`** (moving packages changes import paths → sync conflicts).
- **Do NOT edit `pkg/core` directly.** It is the (obfuscated) license gate. It is
  toggled by the `LICENSE_GATE_ENABLED` env var (default `true`; set `false` to run
  independently, without the activation gate or remote heartbeat). Wire changes go
  in `cmd/evolution-go/main.go` / `pkg/config`, not in `pkg/core`.
- **Do NOT rebrand `manager/dist/`** (the React console UI). The LICENSE adds a
  condition (§1a) forbidding removal/modification of the Evolution logo and
  copyright in the console frontend. Rebranding is backend/display-only.
- **Keep Apache-2.0 attribution** in `LICENSE` and `NOTICE`. "Evolution",
  "Evolution Foundation", and "Evolution Go" are trademarks — do not use them for
  OmniWA GO branding.
- **Write all documentation in English.**

When rebranding or customizing, concentrate changes in a few files (README,
Makefile `APP_NAME`, `main.go` banner/`@title`, `.env.example`, `docker/`) so
upstream syncs stay low-conflict.

## Contribution workflow

`main` is protected — **never push to `main` directly**.

Every task that changes repository files must be completed on a new branch and
delivered through a pull request targeting `main`. Do not leave completed
changes only in the local working tree. A task is not complete until the branch
is pushed and the PR URL is reported. Read-only investigations do not require a
PR.

1. Branch from `main`: `git switch -c <type>/<short-description>`.
2. Make a focused change; run build/vet/test.
3. Open a PR against `main`; the `build / vet / test` check must be green.
4. Squash-merge.

Use [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`,
`chore:`, `docs:`, `ci:`, `refactor:`, `test:`.

## Upstream sync

This fork merges `upstream/main` periodically. Follow
[docs/SYNC.md](./docs/SYNC.md). Expected conflicts are limited to a few branding
files and `main.go`; conflicts in `import` blocks mean the module path was changed
— stop and revert that.

## Security

- Auth is via the `apikey` header (`GLOBAL_API_KEY` for admin routes). Never log
  tokens, keys, or secrets.
- Use parameterized queries — never build SQL with string formatting.
- Report vulnerabilities per [SECURITY.md](./SECURITY.md); do not open public issues.
