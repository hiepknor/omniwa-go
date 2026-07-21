# Contributing to OmniWA GO

Thanks for your interest in improving OmniWA GO.

## Development setup

Requirements: Go (version in `go.mod`) and Docker.

Run the app locally with the dev stack (Postgres + RabbitMQ + MinIO, license
gate disabled):

```bash
cd docker
docker compose -f docker-compose.dev.yml up -d
curl http://localhost:4000/health
```

Or run natively with `make dev` (copy `.env.example` to `.env` first). See
`docker/README.md` for all compose options.

## Workflow

`main` is protected: changes land via pull request and CI must pass. Do not push
to `main` directly.

1. Branch from `main`: `git switch -c <type>/<short-description>`.
2. Make a focused change.
3. Verify locally:
   ```bash
   go build ./...
   go vet ./...
   go test ./...
   ```
4. Open a PR against `main`. The `build / vet / test` check must be green.
5. Squash-merge once approved.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/): `feat:`,
`fix:`, `chore:`, `docs:`, `ci:`, `refactor:`, `test:`. Keep the subject
imperative and short; explain the "why" in the body.

## Code style

- Format with `gofmt` (`make fmt`) and keep `go vet` clean.
- Match the style and structure of the surrounding code.
- Add tests for new behavior where practical.

## Keeping the fork sync-friendly

OmniWA GO is a fork of
[Evolution Go](https://github.com/evolution-foundation/evolution-go) and merges
upstream releases. To keep syncs low-conflict:

- Do **not** change the Go module path
  (`github.com/evolution-foundation/evolution-go`) or the `cmd/evolution-go/`
  directory.
- Keep rebranding and customization concentrated in a few files.

See `docs/SYNC.md` for the upstream sync process.

## Security issues

Do not open public issues for vulnerabilities — see [SECURITY.md](./SECURITY.md).
