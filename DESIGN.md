# `apigw` — Migration Plan (GitHub-CLI-style rewrite)

> Migrate the current bash-based API Gateway Installer (~4,700 lines of shell + JS)
> into a single Go binary modeled after the GitHub CLI (`gh`).

## Guiding principles

1. **Feature parity first.** Everything the current bash installer can do, the
   new tool must do — no regressions, no "we'll add it later." The §8 mapping
   table is the contract.
2. **HTTPS is first-class, not a patch.** TLS setup (Let's Encrypt / DuckDNS /
   self-signed) is offered in the install wizard AND available as a single
   command (`apigw tls enable …`) at any point later. Switching strategies and
   adding domains stays one command — no editing nginx by hand, no separate
   certbot ritual.
3. **Better UX everywhere.** Single binary, interactive when run by a human,
   scriptable with `--json` and `--config` when run by CI. Idempotent —
   re-running never breaks state.
4. **Live everywhere — no manual refresh.** Logs, deploy progress, service
   status, webhook events — all stream in real time over SSE / WebSocket /
   `tail -f`-style. The dashboard never needs F5. The CLI shows progress as
   it happens, not after. Build output streams line-by-line during
   `apigw deploy run`. See §5c for the streaming contract.
5. **No half-migrations.** A user on the old bash installer upgrades to
   `apigw` with one command, keeps their APIs, deployments, and webhooks.

---

## 1. Why migrate

Current pain points (from the existing codebase):

| Problem in bash version                              | Where                              |
| ---------------------------------------------------- | ---------------------------------- |
| 1,775-line monolithic `install.sh`, no unit tests    | `install.sh`                       |
| Logic scattered across `install.sh` + 3 modules + extended CLI script | `modules/*.sh`, `scripts/api-manage-extended` |
| `apis.json` edited via `jq` without file locks → races | `modules/*.sh`                   |
| HTTPS / Let's Encrypt added as out-of-band patches   | Not in installer at all            |
| Two Node.js servers (`server.js` 765 LoC, `webhook-server.js`) running separately, no shared state | `web-ui/`     |
| Hardcoded paths (`/etc/api-gateway`, `/opt/api-gateway`, `/usr/local/bin`) everywhere | `install.sh`              |
| No idempotency guarantees; reinstall sometimes leaves stale systemd units | `install.sh`               |
| No dry-run, no plan-mode, no rollback                | —                                  |
| OS-specific assumptions (Debian/Ubuntu only)         | `safe_apt()`                       |
| No structured logging, no telemetry, no `--json` output | —                              |

What `gh` solves that we want:

- **Single static binary** — `curl -L … -o /usr/local/bin/apigw && chmod +x`
- **Discoverable subcommand tree** — `apigw <noun> <verb> [flags]`
- **Interactive prompts when TTY, flags when scripted** — same command, both modes
- **`--json` everywhere** — machine-readable output for CI / piping
- **Extensions** — `apigw extension install user/apigw-foo` (post-MVP)
- **Beautiful output** — colors, spinners, tables, progress bars
- **Tested** — Go tests + golden files, CI on every PR

---

## 2. Tech stack

| Layer                | Library                                | Used by                |
| -------------------- | -------------------------------------- | ---------------------- |
| CLI framework        | `spf13/cobra`                          | `gh`, `kubectl`, `hugo`|
| Config files         | `spf13/viper` (YAML + env + flags)     | `gh`, `helm`           |
| Interactive prompts  | `charmbracelet/huh`                    | modern Charm stack     |
| Styling / colors     | `charmbracelet/lipgloss`               | `gh`, `lazygit`        |
| Spinners / progress  | `charmbracelet/bubbletea` + `bubbles`  | `gh`, `soft-serve`     |
| Tables               | `charmbracelet/lipgloss/table`         | Charm                  |
| Structured logging   | `log/slog` (stdlib, Go 1.21+)          | stdlib                 |
| HTTP client          | `net/http` + `hashicorp/go-retryablehttp` | gh, terraform       |
| YAML                 | `goccy/go-yaml`                        | modern alternative     |
| File locks           | `gofrs/flock`                          | helm, terraform        |
| systemd interaction  | `coreos/go-systemd/v22`                | etcd, k8s              |
| Cert renewal         | shell out to `certbot` (don't reinvent) | —                     |
| GitHub webhook HMAC  | `crypto/hmac` (stdlib)                 | stdlib                 |
| Embedded assets      | `embed` (stdlib)                       | stdlib                 |
| Test runner          | `testing` + `testify/require`          | everyone               |
| Golden-file tests    | `hexops/autogold`                      | sourcegraph            |

Build artifact: **~15 MB static binary**, no runtime deps. Cross-compiled for:
- `linux/amd64`
- `linux/arm64` (Orange Pi 5, Raspberry Pi 4/5, AWS Graviton)
- `linux/arm/v7` (older Pi)
- `darwin/amd64`, `darwin/arm64` (for local dev / dry-runs)

---

## 3. Repository layout (post-migration)

```
api-gateway-installer/
├── cmd/
│   └── apigw/
│       └── main.go                  # cobra root, version, telemetry init
├── internal/
│   ├── cmd/                         # one file per subcommand tree
│   │   ├── root.go                  # `apigw` root command
│   │   ├── install/                 # `apigw install`
│   │   │   ├── install.go
│   │   │   ├── wizard.go            # interactive steps (huh)
│   │   │   └── plan.go              # `--dry-run` / `--plan`
│   │   ├── api/                     # `apigw api …`
│   │   ├── deploy/                  # `apigw deploy …`
│   │   ├── webhook/                 # `apigw webhook …`
│   │   ├── tls/                     # `apigw tls …`
│   │   ├── dashboard/               # `apigw dashboard …`
│   │   ├── status/                  # `apigw status`
│   │   ├── doctor/                  # `apigw doctor`
│   │   ├── backup/                  # `apigw backup`, `apigw restore`
│   │   └── auth/                    # `apigw auth login` (for hosted dashboard, later)
│   ├── core/                        # domain logic, no I/O dependencies
│   │   ├── apis/                    # apis.json model + CRUD with flock
│   │   ├── nginx/                   # config generation from templates
│   │   ├── tls/                     # LE / DuckDNS / self-signed strategies
│   │   ├── deploy/                  # git clone, runtime detect, systemd write
│   │   ├── dashboard/               # http server, SSE log stream
│   │   └── webhook/                 # HMAC verify, queue, dispatch
│   ├── platform/                    # OS abstractions
│   │   ├── pkgmgr/                  # apt / dnf / pacman (detect + install)
│   │   ├── service/                 # systemd unit gen + reload (go-systemd)
│   │   ├── firewall/                # ufw / firewalld wrappers
│   │   ├── ports/                   # is-listening / is-free
│   │   └── osdetect/                # debian 12, ubuntu 24.04, etc.
│   ├── config/                      # /etc/apigw/config.yml load/save
│   ├── ui/                          # lipgloss styles, table helpers, spinner
│   ├── log/                         # slog setup, file + journald handlers
│   └── version/                     # ldflags-injected version info
├── pkg/                             # (kept empty until something needs export)
├── assets/                          # embedded templates
│   ├── nginx/
│   │   ├── server.tmpl
│   │   ├── api-location.tmpl
│   │   └── tls-server.tmpl
│   ├── systemd/
│   │   ├── apigw-deploy@.service.tmpl
│   │   ├── apigw-dashboard.service.tmpl
│   │   └── apigw-webhook.service.tmpl
│   └── dashboard/                   # embedded static UI (replaces web-ui/)
│       ├── index.html
│       └── assets/
├── docs/
│   ├── cli/                         # auto-generated from cobra (`apigw gen-docs`)
│   ├── install.md
│   ├── migration-from-bash.md
│   └── architecture.md
├── scripts/
│   ├── install.sh                   # thin curl-shim that downloads the binary
│   └── release.sh
├── test/
│   ├── e2e/                         # docker-based end-to-end (ubuntu 24.04 image)
│   └── fixtures/
├── .github/
│   └── workflows/
│       ├── ci.yml                   # test + lint + cross-compile
│       └── release.yml              # goreleaser
├── .goreleaser.yml
├── Makefile
├── go.mod
└── README.md
```

The old `install.sh`, `modules/`, `scripts/api-manage-extended`, and `web-ui/`
stay in the repo on a `legacy/` branch for one release cycle, then get deleted.

---

## 4. CLI surface (mirrors `gh <noun> <verb>`)

```
apigw                                       # show help + status overview

# First-time setup
apigw install                               # interactive wizard
apigw install --plan                        # show what would be installed, exit
apigw install --config install.yml          # unattended install from file
apigw install --print-config > install.yml  # save current wizard answers
apigw uninstall                             # interactive, with auto-backup

# API CRUD (replaces `api-manage`)
apigw api list [--json]
apigw api add <name> --port <p> [--path <p>] [--description <text>]
apigw api remove <name>
apigw api enable <name> | disable <name>
apigw api reload                            # nginx -t && reload

# Deployments (replaces `deploy …`)
apigw deploy list [--json]
apigw deploy add <name> --repo <url> [--branch main] --port <p> \
                       [--build "npm ci && npm run build"] \
                       [--start "npm start"] \
                       [--runtime auto|node|python|go|docker]
apigw deploy remove <name>
apigw deploy run <name>                     # force redeploy
apigw deploy logs <name> [--follow] [--lines 100]
apigw deploy status [<name>] [--json]
apigw deploy ssh-key                        # show / regenerate deploy SSH key

# Webhooks (replaces `webhook …`)
apigw webhook list [--json]
apigw webhook status
apigw webhook url <deployment>              # print public webhook URL
apigw webhook setup <deployment>            # interactive: paste secret to GitHub
apigw webhook rotate-secret <deployment>

# TLS (NEW — fixes the out-of-band certbot setup)
apigw tls status
apigw tls enable letsencrypt --domain <fqdn> --email <addr>
apigw tls enable duckdns     --subdomain <s>   --token <t> --email <addr>
apigw tls enable self-signed --cn <name>
apigw tls renew [--dry-run]
apigw tls disable

# Dashboard
apigw dashboard start | stop | restart | status
apigw dashboard url
apigw dashboard open                        # opens in browser (xdg-open / open)

# Diagnostics
apigw status [--json]                       # one-shot health overview
apigw doctor                                # actionable checklist of issues
apigw logs [--service nginx|webhook|dashboard|deploy] [--follow]

# Backups
apigw backup [--out backup.tar.gz]
apigw restore <backup.tar.gz> [--force]

# Self-management
apigw upgrade                               # in-place update of the binary
apigw version
apigw completion bash|zsh|fish              # shell completions

# Extensions (post-MVP, like `gh extension`)
apigw extension list
apigw extension install <repo>
apigw extension remove <name>
```

Every command supports:
- `--json` for structured output
- `--quiet` / `-q` to suppress non-essential output
- `--verbose` / `-v` for debug logging
- `--no-color` (also respects `NO_COLOR` env var)
- `--config <path>` to use a non-default config

---

## 5a. HTTPS is convenient — at install time OR later

Both flows lead to the same end state, and they're interchangeable: you can
skip TLS during install and add it later, or set it up during install and
switch strategies later. The CLI owns the whole lifecycle — no certbot
incantations, no editing nginx, no out-of-band patches.

### Flow A — during install (the recommended path)

The wizard's HTTPS step is **Step 3 of 7**. The user picks one of:

- **Let's Encrypt** — needs a public domain pointing at the box. Wizard
  validates DNS resolves to the public IP before continuing.
- **DuckDNS** — needs a DuckDNS subdomain + token (free, no domain purchase).
  Wizard updates the subdomain to the public IP, then gets a Let's Encrypt
  cert via DNS-01.
- **Self-signed** — works immediately, no DNS / network requirements. Marked
  "dev/local only" with a warning.
- **Skip** — HTTP only. Wizard reminds the user: "you can enable TLS anytime
  with `apigw tls enable`."

In every case, `apigw` writes the cert paths, sets up auto-renewal (systemd
timer + deploy-hook that reloads nginx), and wires nginx to listen on both
80 (redirect) and 443. **The user types nothing about certbot.**

### Flow B — adding TLS later (the "convenient post-install" path)

If the user picked "Skip" during install, or wants to switch from self-signed
to Let's Encrypt later, it's one command. Examples:

```bash
# Enable Let's Encrypt for a real domain
apigw tls enable letsencrypt --domain api.example.com --email me@example.com

# Or use a free DuckDNS subdomain (no domain purchase needed)
apigw tls enable duckdns \
  --subdomain orangepiapi \
  --token fe1591ff-26c3-4996-b433-a31a0c6560a7 \
  --email me@example.com

# Local / dev only
apigw tls enable self-signed --cn orangepi.local

# Add a second domain to an existing Let's Encrypt cert
apigw tls add-domain api2.example.com

# Manually renew (auto-renewal is already set up)
apigw tls renew

# Check what's configured
apigw tls status
# → Strategy:   letsencrypt
#   Domain(s):  api.example.com
#   Expires:    2026-09-02 (90 days)
#   Auto-renew: enabled (systemd timer, checks daily)
#   Last renew: 2026-06-04 18:30 UTC (success)
```

Each command runs interactively when there's a TTY — the wizard asks for
anything you didn't pass as a flag, validates it (DNS check, port-80
reachable for HTTP-01, DuckDNS API token works), and shows progress.

### What apigw does under the hood (so you don't have to)

For every TLS strategy, `apigw`:

1. Installs `certbot` if missing (via the platform's package manager).
2. Generates / writes DuckDNS hooks at `/etc/letsencrypt/duckdns/` with
   600 perms (matches what we set up manually for the FoodManager pi).
3. Acquires the cert (HTTP-01 for normal domains, DNS-01 for DuckDNS).
4. Rewrites the nginx server block from a template: dual-listen on 80+443,
   HSTS header, redirect HTTP→HTTPS, proper `ssl_certificate` paths.
5. Validates with `nginx -t` before reloading; rollback if invalid.
6. Installs a `--deploy-hook` so renewals automatically reload nginx.
7. Verifies the systemd `certbot.timer` is enabled.
8. Runs `certbot renew --dry-run` once to prove auto-renewal works.

This is exactly the manual sequence we did for `orangepiapi.duckdns.org`,
collapsed into one command.

---

## 5c. Live streaming — the "no refresh" contract

This is a first-class requirement, not a polish item. Every place that shows
state pushes updates as they happen — both in the CLI (when attached to a
terminal) and in the dashboard (over the network). The only "refresh button"
in the entire product is the browser one, and the user should never need it.

### What streams live

| Surface                                        | Source                                   | Transport                              | Latency target |
| ---------------------------------------------- | ---------------------------------------- | -------------------------------------- | -------------- |
| `apigw deploy logs <name> --follow`            | journald + child process stdout/stderr   | inotify on log file + `journalctl -f`  | <100 ms        |
| `apigw deploy run <name>` (live build output)  | git clone → build → start, all stages    | piped stdout, line-buffered            | line-by-line   |
| `apigw logs --service nginx --follow`          | nginx access/error logs                  | inotify watcher                        | <100 ms        |
| `apigw status --watch`                         | service states + ports + cert expiry     | re-check on systemd D-Bus signals      | <500 ms        |
| Dashboard: deploy log viewer                   | same as `--follow`                       | **Server-Sent Events** (SSE)           | <200 ms RTT    |
| Dashboard: deploy list / status badges         | systemd D-Bus + apigw internal events    | SSE channel `/events?topic=deploy`     | <200 ms        |
| Dashboard: webhook activity feed               | webhook handler emits events             | SSE channel `/events?topic=webhook`    | <200 ms        |
| Dashboard: nginx request rate / latency        | access log tail → parser                 | SSE with 1 s aggregation               | 1 s window     |
| Dashboard: TLS expiry countdown                | cert file watched + parsed on change     | SSE on the same `/events` stream       | event-driven   |

### Why SSE (not polling, not WebSocket-everywhere)

- **No polling.** Polling means lag, wasted bandwidth, stale UI, and refresh
  buttons. Banned across the product. If we need a value, the server pushes
  it.
- **SSE over WebSocket** for log/event streams: SSE is one-way (server →
  browser), reconnects automatically with `Last-Event-ID`, traverses proxies
  cleanly, doesn't need a separate protocol upgrade, and is trivial in Go
  (`http.Flusher`). WebSocket only where we need browser→server messages —
  none of our current features need that.
- **One persistent SSE connection per dashboard tab**, multiplexing topics
  via event types (`deploy:foo:log`, `webhook:received`, `tls:expiry`,
  `status:nginx`). Saves connections, keeps the server's fan-out cheap.

### Implementation outline

```
                    ┌──────────────────────────────────────────┐
                    │             apigw (single binary)        │
                    │                                          │
   inotify ──────▶  │  log/tail.go      ─────┐                 │
   journald  ─────▶ │  log/journald.go  ─────┤                 │
   systemd D-Bus ─▶ │  events/systemd.go ────┼──▶  hub.go      │
   nginx access ──▶ │  log/nginx.go     ─────┤    (pub/sub)    │
   webhook recv ─▶  │  webhook/server.go ────┘         │       │
                    │                                  ▼       │
                    │           ┌──────────────────────────┐   │
                    │           │  internal/events/hub.go  │   │
                    │           │  topics, subscribers,    │   │
                    │           │  ring buffer (last 1k)   │   │
                    │           └─────────┬──────────────┬─┘   │
                    │                     │              │     │
                    │      ┌──────────────▼─┐   ┌────────▼────┐│
                    │      │ CLI consumer   │   │ SSE handler ││
                    │      │ (--follow,     │   │ /events     ││
                    │      │  --watch)      │   │             ││
                    │      └────────────────┘   └──────┬──────┘│
                    └──────────────────────────────────┼───────┘
                                                       │
                                              ┌────────▼────────┐
                                              │ Dashboard (web) │
                                              │ EventSource     │
                                              │ + DOM updates   │
                                              └─────────────────┘
```

- **`events/hub.go`** is the central pub/sub. Producers (log tailers, systemd
  watchers, webhook handler) publish typed events to topics. Consumers (CLI
  `--follow`, SSE handler, dashboard) subscribe to topics they care about.
- **Ring buffer per topic** (last 1k events). New subscribers get the
  backlog instantly — no "loading…" blank state.
- **Backpressure**: per-subscriber bounded channel; slow consumer gets
  dropped events + a `dropped: N` summary, not a server stall.
- **Reconnect**: SSE `Last-Event-ID` header lets the dashboard catch up
  after a brief disconnect (Wi-Fi blip, page resume from sleep) without a
  full refresh.

### Dashboard rendering — instant DOM updates

The dashboard is a thin client over the event stream. No SPA framework
needed for v1 — vanilla JS + `EventSource`:

```js
const es = new EventSource("/events?topics=deploy,webhook,tls,status");

es.addEventListener("deploy:log", (e) => {
  const { name, line, level } = JSON.parse(e.data);
  appendLine(`#deploy-${name}-log`, line, level);   // auto-scroll if pinned
});

es.addEventListener("deploy:status", (e) => {
  const { name, status, sha } = JSON.parse(e.data);
  updateBadge(`#deploy-${name}-badge`, status, sha); // CSS transitions only
});

es.addEventListener("tls:expiry", (e) => {
  const { domain, daysLeft } = JSON.parse(e.data);
  updateCountdown(`#tls-${domain}`, daysLeft);
});
```

Animations are CSS-only (transitions on color/opacity), so updates feel
smooth even at 10 events/sec. The page is rendered server-side once; after
that, **the DOM only mutates from event handlers — no client-side polling,
no full re-renders, no F5.**

### CLI streaming — the `--follow` and `--watch` flags

- **`--follow` / `-f`** on log commands behaves exactly like `tail -f`:
  prints what's there, then blocks and streams new lines as they arrive.
  Ctrl-C to exit. Works over the same `events/hub.go` — the CLI is just
  another subscriber.
- **`--watch` / `-w`** on status/list commands re-renders the table in
  place when underlying state changes (uses `lipgloss` cursor positioning,
  not naive clear-screen).

### Performance budget (so "smooth" isn't subjective)

- SSE event RTT: **<200 ms** for events originating on the same box.
- CLI `--follow` log line latency: **<100 ms** from disk write to terminal.
- Dashboard tab can sustain **>1000 events/sec** without dropping frames
  (line-by-line build output during a `npm install` is the stress case).
- Memory: ring buffer is capped per topic; no unbounded growth.
- CPU: idle state <0.1% on Orange Pi 5 (event hub is goroutine-per-topic,
  no busy loops).

These numbers are part of the Phase 5 acceptance criteria and get tested in
the E2E suite (Phase 10).

---

## 5b. Wizard UX (the "wow" moment)

The `apigw install` wizard is the showcase. Every step:

1. **Header** showing progress (`Step 3 of 7 · HTTPS Setup`)
2. **Inline explanation** (2–4 sentences) — what this step does and why it matters
3. **Prompt** (`huh.Select` / `huh.Input` / `huh.Confirm`)
4. **Live validation** (port already in use? DNS resolves? domain reachable?)
5. **`Press ?` for more detail** opens a longer explanation pane

Example (HTTPS step):

```
┌──────────────────────────────────────────────────────────┐
│ Step 3 of 7 · HTTPS Setup                                │
└──────────────────────────────────────────────────────────┘

HTTPS protects requests in transit and is required by browsers and
mobile app stores for production use. Pick the approach that fits
your situation — you can change it later with `apigw tls enable`.

  ▸ Let's Encrypt (free, trusted, needs a public domain)
    DuckDNS (free *.duckdns.org subdomain — no domain needed)
    Self-signed (works locally only; clients must trust the cert)
    Skip TLS (HTTP only — not recommended)

  Press ? for details · ↑/↓ to choose · Enter to select
```

After every step the wizard appends a line to a running **install plan** on
the right side. At the end the user sees the full plan and confirms before
anything touches disk. Hitting `Esc` rewinds one step. `apigw install --plan`
prints the plan and exits without executing — great for review / CI.

---

## 6. Domain model

### `apis.json` → typed Go struct, single source of truth

```go
type Config struct {
    Version   int        `yaml:"version"`     // schema version, for migrations
    Listen    ListenSpec `yaml:"listen"`      // port, TLS, server_name
    APIs      []API      `yaml:"apis"`
    Deploys   []Deploy   `yaml:"deployments"`
    Webhook   Webhook    `yaml:"webhook"`
    Dashboard Dashboard  `yaml:"dashboard"`
    TLS       TLSConfig  `yaml:"tls"`
}
```

Stored at `/etc/apigw/config.yml`. Every mutation:
1. Acquire `flock` on `/etc/apigw/.lock`
2. Read + validate current config
3. Apply change
4. Write atomically (write to `config.yml.new`, fsync, rename)
5. Regenerate nginx config from template
6. `nginx -t` → if fail, rollback both files
7. `systemctl reload nginx`
8. Release lock

**Migrations**: on version mismatch, `apigw` runs a migration step before
loading. The first migration converts the existing bash-installer's
`/etc/api-gateway/apis.json` into the new YAML format.

### nginx config: generated from `text/template`, never hand-edited

The generated config has a banner:

```
# MANAGED BY apigw — do not edit.
# To make changes: `apigw api add|remove` or edit /etc/apigw/config.yml
# Generated at: 2026-06-04T18:30:00Z by apigw v0.3.0
```

A pre-flight check (`apigw doctor`) verifies the banner is still present and
the file hash matches what apigw wrote — detects hand-edits and warns.

---

## 7. Phases (incremental delivery, each phase is a usable build)

TLS is bumped up to Phase 2 — it's a headline feature, not an afterthought.
Every phase ships behind a `v0.X.0` tag and is usable on its own.

| Phase | Scope                                                                                  | Output                                                   | Effort  |
| ----- | -------------------------------------------------------------------------------------- | -------------------------------------------------------- | ------- |
| **0** | Bootstrap repo: cobra root, lipgloss, huh hello-world, CI cross-compile, goreleaser   | `apigw version` works, signed releases publish to GH    | 1 day   |
| **1** | `apigw install` (nginx + apis.json + auto-reload) + `apigw api {list,add,remove,enable,disable,reload}` with `--json` | Feature parity with `install.sh` + `api-manage` (HTTP only) | 5 days  |
| **2** | **`apigw tls enable {letsencrypt,duckdns,self-signed}` + `apigw tls renew/status/add-domain/disable` + wizard integration in `apigw install`** | **HTTPS first-class — at install time OR later, one command either way** | 4 days |
| **3** | `apigw deploy {add,run,logs,status,remove,ssh-key}` with runtime auto-detect          | Replaces `deployment-manager.sh` (591 LoC of bash)       | 4 days  |
| **4** | `apigw webhook` + embedded HTTP server (replaces `webhook-server.js`)                 | One binary handles webhooks, no Node.js                  | 3 days  |
| **5** | `apigw dashboard` (embedded UI) + `events/hub.go` pub/sub + SSE streaming for logs, deploy status, webhook activity, TLS expiry. CLI `--follow`/`--watch` use the same hub | **Live everywhere — no page refresh, no polling.** Replaces 765-LoC `server.js` | 6 days  |
| **6** | `apigw ai add {ollama,localai,vllm}` + `ai list/remove/pull`                          | Replaces `ai_*` functions from `api-manage-extended`     | 2 days  |
| **7** | `apigw status`, `apigw doctor`, `apigw backup/restore`, `apigw logs`                  | Operator-friendly diagnostics                            | 2 days  |
| **8** | Migration tool: import existing `/etc/api-gateway/apis.json` → `/etc/apigw/config.yml` + `api-manage` symlink shim | Existing bash installs upgrade cleanly, no manual steps | 2 days |
| **9** | `apigw completion`, `apigw upgrade`, docs (`apigw gen-docs` → `docs/cli/`)            | Polish + auto-generated CLI reference                    | 2 days  |
| **10**| End-to-end docker tests on Debian 12, Ubuntu 22.04 / 24.04                            | `make e2e` passes in CI                                  | 3 days  |

**Total ≈ 34 dev-days** (one developer, ~6–7 weeks calendar).

---

## 8. What replaces what — feature parity guarantee

### File-level mapping

| Bash artifact                              | Go replacement                                  |
| ------------------------------------------ | ----------------------------------------------- |
| `install.sh` (1775 LoC)                    | `internal/cmd/install/*` + `internal/core/*`    |
| `install-cli.sh` (curl shim)               | `scripts/install.sh` (downloads release binary) |
| `uninstall.sh` (259 LoC)                   | `apigw uninstall`                               |
| `modules/common.sh` (logging, apt wrapper) | `internal/ui/*`, `internal/platform/pkgmgr/*`   |
| `modules/deployment-manager.sh` (591 LoC)  | `internal/core/deploy/*`                        |
| `modules/webhook-handler.sh` (234 LoC)     | `internal/core/webhook/*`                       |
| `scripts/api-manage-extended` (869 LoC)    | `apigw api`, `apigw deploy`, `apigw webhook` …  |
| `web-ui/server.js` (765 LoC Node)          | `internal/core/dashboard/` + embedded assets    |
| `web-ui/webhook-server.js` (226 LoC Node)  | folded into webhook subcommand (same binary)    |

Net: ~4,700 LoC of bash+JS → ~3,500 LoC of Go + tests. No Node.js runtime
dependency on the server.

### Command-level parity matrix (no feature is lost)

Every command from `api-manage-extended` has an `apigw` equivalent. Most are
also **better** — see the right column.

| Today (`api-manage-extended`)               | Tomorrow (`apigw`)                              | What's better                                            |
| ------------------------------------------- | ----------------------------------------------- | -------------------------------------------------------- |
| **API management**                          |                                                 |                                                          |
| `add <name> <port> [path]`                  | `apigw api add <name> --port <p> [--path <p>]`  | Named flags, `--json` output, validation before write    |
| `remove <name>`                             | `apigw api remove <name>`                       | Confirmation prompt, single-binary atomic write          |
| `list`                                      | `apigw api list [--json]`                       | Table view + machine-readable mode                       |
| `enable <name>` / `disable <name>`          | same                                            | Atomic + nginx pre-flight validate                       |
| `reload`                                    | `apigw api reload`                              | Validates before reload, rolls back on failure           |
| **Deployments**                             |                                                 |                                                          |
| `deploy setup-private`                      | `apigw deploy ssh-key [--regenerate]`           | Idempotent, prints public key, copies to clipboard if TTY|
| `deploy add <name> <repo> [branch] <port> …`| `apigw deploy add` with named flags             | Auto-detects runtime; clear error if repo unreachable    |
| `deploy remove <name>`                      | same                                            | Cleans up systemd units it owns                          |
| `deploy list`                               | `apigw deploy list [--json]`                    | Shows last deploy time + status inline                   |
| `deploy status [name]`                      | same, with `--json`                             | Includes git SHA + uptime + memory                       |
| `deploy run <name>`                         | same                                            | Streams build output live, exit code reflects success    |
| `deploy logs <name>`                        | `apigw deploy logs <name> [--follow] [--lines]` | **Live tail with <100 ms latency** — `kubectl logs -f` style, no manual refresh |
| `deploy run <name>`                         | `apigw deploy run <name>`                       | **Build output streams line-by-line** as it happens, not after |
| **Webhooks**                                |                                                 |                                                          |
| `webhook start/stop/status`                 | same                                            | One binary — webhook server is built in, not separate    |
| `webhook url <name>`                        | same                                            | Same                                                     |
| `webhook setup <name>`                      | same                                            | Interactive: walks through GitHub UI steps               |
| —                                           | `apigw webhook rotate-secret <name>`            | **NEW** — rotates HMAC secret + updates GitHub via API   |
| **Dashboard**                               |                                                 |                                                          |
| `dashboard start/stop/status/url`           | same + `apigw dashboard open`                   | **Fully live via SSE** — log viewer, deploy badges, webhook feed, TLS countdown all update instantly. No polling, no refresh button. |
| **AI**                                      |                                                 |                                                          |
| `ai add ollama/localai/vllm [path]`         | same                                            | Detects existing install, doesn't reinstall              |
| `ai list / remove / pull`                   | same                                            | Same                                                     |
| **System**                                  |                                                 |                                                          |
| `status`                                    | `apigw status [--json]`                         | Includes TLS expiry, disk, every service, deploy health  |
| `logs [service]`                            | `apigw logs [--service x] [--follow]`           | **Live unified stream** across nginx / webhook / dashboard / deploys, single tailable view |
| `status`                                    | `apigw status [--watch]`                        | **`--watch` re-renders in place** when services / cert / ports change — no manual rerun |
| `backup`                                    | `apigw backup [--out <file>]`                   | Includes TLS certs + deploy state, not just `apis.json`  |
| `restore <file>`                            | same                                            | Dry-run mode shows what would change                     |
| **HTTPS (was NOT in the bash installer)**   |                                                 |                                                          |
| — (manual certbot ritual)                   | `apigw tls enable letsencrypt --domain …`       | **NEW first-class** — one command, no manual steps       |
| —                                           | `apigw tls enable duckdns --subdomain … --token …` | **NEW** — free TLS without buying a domain            |
| —                                           | `apigw tls enable self-signed`                  | **NEW** — for dev/local                                  |
| —                                           | `apigw tls renew [--dry-run]`                   | **NEW** — auto-renewal verified at install time          |
| —                                           | `apigw tls status / add-domain / disable`       | **NEW** — full lifecycle managed                         |
| **Install / uninstall**                     |                                                 |                                                          |
| `sudo ./install.sh` (interactive)           | `apigw install` (interactive wizard)            | Plan-mode preview, `--config` for repeatable installs   |
| `sudo ./uninstall.sh`                       | `apigw uninstall`                               | Auto-backup before removal                               |
| —                                           | `apigw doctor`                                  | **NEW** — actionable checklist of detected issues        |
| —                                           | `apigw upgrade`                                 | **NEW** — self-update from GitHub releases               |
| —                                           | `apigw completion bash\|zsh\|fish`              | **NEW** — shell completions                              |

If something from the current bash installer doesn't appear in this table,
it's a parity bug — file an issue and we add it before v1.0.0.

---

## 9. Distribution

Three install paths, same binary:

```bash
# 1. One-liner (matches gh's install style)
curl -fsSL https://apigw.dev/install.sh | sudo bash

# 2. Direct download
curl -fsSL -o apigw \
  https://github.com/<org>/api-gateway-installer/releases/latest/download/apigw_linux_arm64
sudo install -m 0755 apigw /usr/local/bin/

# 3. Homebrew (linuxbrew works too)
brew install <org>/tap/apigw
```

Releases via **goreleaser**: signed checksums, SBOM, Homebrew tap auto-update,
deb/rpm packages for `apt install` / `dnf install`.

`apigw upgrade` checks GitHub Releases API and self-replaces (like `gh
extension upgrade`).

---

## 10. Open decisions (block Phase 0 start)

These need answers before writing the first line of Go. Defaults in **bold**.

1. **Repo layout** — keep Go code in **this repo** alongside legacy bash for
   one cycle, or new `apigw` repo?
2. **Module path** — `github.com/devevghenicernev-png/apigw` based on current
   GitHub user, or move to an org?
3. **Min OS** — **Debian 12 + Ubuntu 22.04 LTS + Ubuntu 24.04 LTS**, or also
   support Debian 11 / Ubuntu 20.04 / RHEL?
4. **License** — keep current free-to-use, switch to **MIT or Apache-2.0**?
5. **Binary name** — `apigw` (short), `api-gateway` (descriptive), keep
   `api-manage` (compatible with existing muscle memory)? Recommended:
   **`apigw`** + a `api-manage` symlink shim for one release.
6. **Telemetry** — opt-in anonymous usage stats (like `gh`), or none?
7. **Hosted dashboard / auth** — out of scope for v1, or wanted? (`gh` has
   `gh auth login` for the GitHub API; we'd have `apigw auth login` for a
   future hosted control plane.)

---

## 11. Definition of done (v1.0.0)

- [ ] `curl … | sudo bash` installs `apigw` on a fresh Debian 12 box
- [ ] `apigw install` wizard sets up nginx + TLS + dashboard in <5 min
- [ ] `apigw install --config x.yml` is reproducible (same input → same state)
- [ ] `apigw deploy add` clones, builds, and exposes a sample Node.js repo
- [ ] GitHub webhook redeploys on push (HMAC verified)
- [ ] `apigw status --json` returns valid JSON usable in scripts
- [ ] `apigw deploy logs --follow` streams lines with <100 ms latency
- [ ] Dashboard updates live via SSE: log viewer, deploy status, webhook
      activity, TLS countdown — **no page refresh needed, no polling**
- [ ] Dashboard survives a Wi-Fi blip and reconnects via `Last-Event-ID`
      without losing events
- [ ] `apigw status --watch` re-renders in place on state changes
- [ ] `apigw uninstall` leaves the box in the exact pre-install state
- [ ] E2E tests pass on Debian 12, Ubuntu 22.04, Ubuntu 24.04 in CI
- [ ] `docs/cli/` is auto-generated and committed
- [ ] Bash installer is removed from `main` (kept on `legacy/` branch)
- [ ] Migration guide for existing users is published

---

## 12. Risks & mitigations

| Risk                                          | Mitigation                                                |
| --------------------------------------------- | --------------------------------------------------------- |
| User edits `/etc/nginx/sites-enabled/apis` by hand | Detect via banner+hash, `apigw doctor` warns + offers reconcile |
| certbot DNS-01 hook is fragile (sleep 30s)    | Use `lego` Go library directly OR keep shell-out but with proper retry |
| systemd units left orphaned after upgrade     | Track owned units in config, `apigw uninstall` removes only those |
| ARM build differs subtly (glibc, musl)        | Use `goreleaser` with multi-arch matrix; static link with `CGO_ENABLED=0` |
| Migration from old `/etc/api-gateway/apis.json` corrupts data | Always snapshot to `backup.tar.gz` before migrating |
| Users on Debian 11 with old systemd           | Document min systemd version (≥247), refuse with clear error |

---

## 13. Next action

When the open decisions in §10 are answered, **Phase 0** is one PR:

1. `go mod init github.com/<owner>/apigw`
2. `cmd/apigw/main.go` with cobra root + `version` subcommand
3. `Makefile` with `build`, `test`, `lint`, `release` targets
4. `.github/workflows/ci.yml` — go test + golangci-lint + cross-compile
5. `.goreleaser.yml` — linux/amd64, linux/arm64, linux/arm
6. First tagged release: `v0.0.1` — `apigw version` works, nothing else

Everything after that ships behind a `v0.X.0` tag, parallel to the bash
installer, until `v1.0.0` makes the cutover.
