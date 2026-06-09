# Contributing to apigw

Thanks for considering a contribution. `apigw` is a single static Go
binary that installs and operates an nginx-fronted gateway. Bug reports,
docs fixes, and well-scoped features are all welcome.

## Quick start

```sh
git clone https://github.com/devevghenicernev-png/apigw
cd apigw

# host build (whatever OS you're on)
make build

# the gates CI enforces — run these before opening a PR
make test            # go test -race -count=1 ./...
go vet ./...
gofmt -l internal cmd test       # must be empty
golangci-lint run --timeout=5m   # v2.12.x

# Linux arm64 / amd64 cross-build (this is what we ship)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/apigw-linux-arm64 ./cmd/apigw
```

## Running e2e tests

The end-to-end suite runs `apigw install` in a privileged systemd
container (Debian 12 / Ubuntu 22 / Ubuntu 24) and drives the full CLI:

```sh
APIGW_E2E_DISTRO=ubuntu22 go test -tags=e2e ./test/e2e/...
```

Docker daemon required. Each distro takes ~3–5 min on a recent laptop.

## What we look for in a PR

- One logical change per PR. A bugfix that drags an unrelated refactor
  along is harder to review and harder to revert.
- A test that fails without your change and passes with it. For
  installer / nginx-render fixes, prefer an e2e test over a unit test.
- An entry in `CHANGELOG.md` under `## Unreleased`. Keep it short and
  user-facing — no `Refactor internal helper foo()` lines.
- Conventional-commit subject:
  `fix(nginx): rollback symlink on validate failure (BUG-1)` etc.
- No `--no-verify`, no skipping CI.

## What we will push back on

- Vendoring extra dependencies for things the stdlib + the existing deps
  already cover. We are deliberately conservative about deps: every one
  becomes a supply-chain surface.
- Generic "framework" abstractions when three call sites are not yet a
  pattern.
- Breaking changes to the admin API (`/api/admin/*`, `/api/v1/admin/*`),
  the config schema, or the CLI flag surface, without a deprecation
  notice in `CHANGELOG.md` first. Even at hobby scale, breaking
  someone's working install is not a fair trade for a slightly cleaner
  internal name.
- Changes that lower a test gate ("flaky, removed") rather than fixing
  the underlying flake.

## DCO / CLA

For now, contributions are accepted under the project's `LICENSE`
(Apache 2.0). We may add a DCO sign-off requirement (`git commit -s`)
before `v1.0`; we will not retroactively require a CLA.

## Security issues

Do **not** open a public issue for security problems. See
[`SECURITY.md`](./SECURITY.md) for the private disclosure channel.

## Style

- Go: idiomatic stdlib, `errors.Is/As` over string matching, structured
  `slog` over `fmt.Printf` for runtime logging, table-driven tests where
  natural.
- Comments: explain *why*, not *what*. The code already says what.
- File layout: business logic under `internal/<package>/`, CLI verbs
  under `internal/cmd/<group>/<verb>/`. Don't add a new top-level
  package without discussing first.

## Questions

Open a GitHub Discussion. For private questions, the security email in
`SECURITY.md` works.
