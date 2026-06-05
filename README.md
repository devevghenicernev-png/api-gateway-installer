# apigw

> Install, manage, and observe an nginx-fronted API gateway from a single
> static Go binary. Replaces the bash `install.sh` + `api-manage` flow with
> a `gh`-style CLI.

## What it does

- **Installs** an nginx gateway in one command: `apigw install` (interactive
  wizard) or `apigw install --config install.yml` (unattended).
- **Routes** custom paths to upstream services: `apigw api add my-app
  --port 3000`.
- **Issues TLS** via Let's Encrypt (HTTP-01 webroot), DuckDNS (DNS-01), or
  self-signed — `apigw tls enable letsencrypt --domain api.example.com
  --email me@example.com`. Auto-renewal via systemd timer.
- **Deploys git repos** with runtime auto-detection (Node / Python / Go /
  Docker / static): `apigw deploy add hello --repo
  https://github.com/owner/hello --port 3000`. GitHub webhooks redeploy on
  push, HMAC verified in constant time.
- **Observes** everything live: SSE-driven dashboard at `/dashboard` shows
  deploy state, log stream, webhook activity, and TLS countdowns — no
  manual refresh. CLI mirrors via `apigw deploy logs --follow`,
  `apigw status --watch`.
- **AI providers** (Ollama, LocalAI, vLLM) registered as upstreams:
  `apigw ai add ollama && apigw ai pull ollama llama3`.
- **Backup / restore** all state to one tar.gz, schema-versioned and
  SHA-256 manifested.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/devevghenicernev-png/apigw/main/scripts/install.sh \
  | sudo sh
```

The shim verifies the release tarball with cosign (keyless GitHub OIDC)
and sha256 before installing to `/usr/local/bin/apigw`. Cosign must be on
PATH; install it via <https://docs.sigstore.dev/cosign/installation/> or
re-run with `APIGW_SKIP_COSIGN=1` (sha256-only, not recommended).

Alternative paths:

```sh
# pin a version
APIGW_VERSION=v0.1.0 curl … | sudo sh

# Homebrew (linuxbrew works too)
brew install devevghenicernev-png/tap/apigw

# direct download (then verify manually)
curl -fsSL -o apigw \
  https://github.com/devevghenicernev-png/apigw/releases/latest/download/apigw_linux_arm64
sudo install -m 0755 apigw /usr/local/bin/
```

## Quick start

```sh
sudo apigw install                          # wizard: ports, dashboard, webhook, TLS
sudo apigw tls enable letsencrypt \
  --domain api.example.com --email me@example.com
sudo apigw api add hello --port 3000        # http://api.example.com/api/hello → 127.0.0.1:3000
sudo apigw deploy add ship \
  --repo https://github.com/me/ship --port 8080
apigw webhook setup ship                    # prints secret + GitHub steps
apigw dashboard open                        # https://api.example.com/dashboard
```

## CLI surface

```
apigw install / uninstall / migrate
apigw api list | add | remove | enable | disable | reload
apigw deploy list | add | remove | run | logs | status | ssh-key
apigw tls status | enable {letsencrypt|duckdns|self-signed} | renew | disable
apigw webhook list | status | url | setup | rotate-secret | start | stop
apigw dashboard start | stop | status | url | open
apigw ai add | list | remove | pull | status
apigw status [--json] [--watch]
apigw doctor
apigw logs [--service nginx|webhook|dashboard|deploy:<name>] [--follow]
apigw backup | restore
apigw upgrade
apigw completion {bash|zsh|fish|powershell}
apigw version
```

Every command supports `--json`, `--quiet`, `--verbose`/`--debug`,
`--no-color` (also `NO_COLOR` env), `--config <path>`, `--yes`, and
`--dry-run` where applicable. See `apigw <cmd> --help` for the per-command
flags and examples.

## Architecture

Single Go binary, ~16K LoC. Runtime dependencies:

- **nginx** (apt: `nginx`, dnf: `nginx`) — proxies traffic
- **systemd** — owns every long-running process apigw creates
- **journalctl** — logs (unless dashboard is up, in which case logs stream
  via SSE)

No Node.js, no Python, no Docker required. The dashboard is embedded
vanilla JS + `EventSource`; the webhook receiver, deploy worker, and TLS
expiry ticker all run in one `apigw dashboard serve` process.

For the full design see [`DESIGN.md`](./DESIGN.md) (specification) and
[`ARCHITECTURE.md`](./ARCHITECTURE.md) (engineering choices with cited
rationales).

## Supported OS

Debian 12, Ubuntu 22.04 LTS, Ubuntu 24.04 LTS. Architectures: amd64,
arm64, armv7 (Raspberry Pi 4/5, Orange Pi 5). Tested in CI on all three
distros nightly.

## Migrating from the bash installer

If `/etc/api-gateway/apis.json` exists on this host:

```sh
sudo apigw migrate
```

The migration packs the existing config + deployment metadata into a
pre-migration backup tarball, translates entries into the new
`/etc/apigw/config.yaml`, renames `apis.json` → `apis.json.migrated-<ts>`
(never deletes), and installs the `api-manage` compatibility symlink so
old scripts keep working. Idempotent — a second run is a no-op.

## License

MIT. See [LICENSE](./LICENSE).
