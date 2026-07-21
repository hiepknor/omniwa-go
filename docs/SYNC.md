# Syncing from upstream (Evolution Go)

OmniWA GO is a downstream fork of
[`evolution-foundation/evolution-go`](https://github.com/evolution-foundation/evolution-go)
(Apache-2.0). This document describes how to pull upstream updates in a stable,
low-conflict way.

## Principles that keep syncing smooth

Merge conflicts scale with how much the fork changes files that upstream also
touches. This fork therefore deliberately:

- **Keeps the Go module path** `github.com/evolution-foundation/evolution-go`
  unchanged (imports are never rewritten) → no conflicts in `import` blocks.
- **Keeps the `cmd/evolution-go/` directory** name → no path conflicts.
- Concentrates the display rebrand in a few files: `README.md`, `Makefile`
  (`APP_NAME`), the banner + `@title` in `cmd/evolution-go/main.go`,
  `.env.example`, and the docker files.
- Does not edit `pkg/core` directly; the license gate is toggled with an env
  flag in `main.go`/`pkg/config` rather than by deleting code in `pkg/core`.

As a result, each sync's conflicts (if any) are limited to a handful of branding
files plus `main.go` — localized and easy to resolve.

## One-time setup (already done)

```bash
git remote add upstream https://github.com/evolution-foundation/evolution-go.git
git fetch upstream
```

Verify the fork still shares history with upstream (must print a commit hash):

```bash
git merge-base HEAD upstream/main
```

## Per-release sync workflow

The sync source is **`upstream/main`** (the stable branch). `upstream/develop`
is the development branch — use it only if you intentionally want unreleased
features.

```bash
git fetch upstream

# Anything new?
git log --oneline main..upstream/main

# Create a sync branch off current main
git switch main && git pull
git switch -c sync/upstream-<X.Y> main

# Merge upstream
git merge upstream/main
```

**Expected** conflicts are limited to: `README.md`, `Makefile`, `.env.example`,
`docker/*`, `NOTICE`, and `cmd/evolution-go/main.go` (banner/`@title` + license
gate wiring). Conflicts in `import` blocks are **not** expected (the module path
is unchanged). If you see import conflicts, someone changed the module path —
stop and investigate.

After resolving, run the checklist below, then verify:

```bash
make swagger                 # regenerate docs if handlers changed
go build ./... && go vet ./... && go test ./...
```

Open a PR `sync/upstream-<X.Y>` → `main`, let CI run, review, then
**squash-merge**. Note in the merged PR which upstream version you merged (cross
-check `VERSION`/`CHANGELOG.md`).

## Post-sync rebrand checklist

Upstream reintroduces "Evolution" branding — make sure it does not overwrite the
rebrand:

- [ ] `grep -rIn "Evolution GO\|Evolution Go" --include="*.go" --include="*.md" .`
      only remains on **valid attribution** lines in `NOTICE`/`README`
      (required by Apache-2.0 §4), not in titles/banners/`@title`/`OS_NAME`.
- [ ] `Makefile`: `APP_NAME` is still the OmniWA name.
- [ ] `cmd/evolution-go/main.go`: `@title` and the log banner are still "OmniWA GO".
- [ ] `.env.example`: `OS_NAME` is still "OmniWA GO".
- [ ] License gate flag: `main.go` still wraps the wiring in `cfg.LicenseGateEnabled`.
- [ ] `git grep -n "evolution-foundation/evolution-go" -- '*.go' | wc -l` is
      **unchanged** (the module path must stay; a different count means an import
      was rewritten unintentionally).

## Legal note

The code is Apache-2.0 and may be modified and redistributed freely, but
"Evolution", "Evolution Foundation", and "Evolution Go" are **trademarks** of
Evolution Foundation. When rebranding:

- Keep `LICENSE`, `NOTICE`, and the original attribution (required by Apache-2.0 §4).
- Do not use the "Evolution" name or logo for OmniWA GO branding.
