# Changelog

All notable changes to `apigw` are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/), versions follow SemVer.

## [Unreleased]

## [0.4.4] — 2026-06-09

Architectural fix. v0.4.1 and v0.4.2 papered over a deeper problem in
the dashboard URL design by adding per-asset `location =` blocks at
gateway root for `/app.css`, `/app.js`, `/favicon.svg`. The dashboard's
**admin API** (`/api/admin/*`, `/api/status`, `/api/logs/*`, `/events`)
was never proxied at all — anyone hitting the dashboard in a browser
got a fully rendered UI but every backend call 404'd. Worse, exposing
dashboard endpoints at gateway root collided with user-defined APIs
under `/api/<service>` and made the operator-vs-user security boundary
fuzzy.

### Changed

- **Dashboard moves entirely under `{Dashboard.Path}/`.** Gateway-root
  pollution is gone. The nginx template now emits ONE
  `location /dashboard/` block with a `rewrite ^/dashboard/(.*)$ /$1
  break;` directive that strips the prefix; the daemon serves itself
  as if mounted at root, so its routes (`/`, `/api/admin/*`,
  `/api/status`, `/events`, `/app.css`, `/app.js`, `/favicon.svg`) do
  not change. A second `location = /dashboard` block returns 301 to
  `/dashboard/` so the trailing slash is enforced consistently.
- **`internal/assets/dashboard/index.html` gets `<base href="/dashboard/">`**
  and all asset references switch to relative (`app.css`, `app.js`,
  `favicon.svg`, `api/admin/sso/login`). The `<base>` pins every
  relative URL in the document — including `fetch()` and
  `EventSource()` calls in app.js — to the dashboard mount.
- **`internal/assets/dashboard/app.js` switches all 19 backend calls
  from absolute to relative.** Every `fetch("/api/admin/X")`,
  `fetch("/api/status")`, `EventSource("/events?...")` etc. is now
  `fetch("api/admin/X")` etc. Browser + `<base>` together resolve them
  under `/dashboard/`, nginx strips the prefix, the daemon sees its
  native routes. No code change needed on the daemon side.
- **`location = /app.css` / `/app.js` / `/favicon.svg` blocks removed
  from the nginx template.** They were the v0.4.1/v0.4.2 workaround
  for the absolute-URL design; v0.4.4 fixes the root cause and the
  workaround is no longer needed.

### Tests

- `TestRender_DashboardMount` pins the new shape: 301 redirect, prefix
  rewrite, single proxy, no leaked per-asset locations.
- `TestRender_DashboardMount_OmittedWhenDisabled` ensures the mount is
  only emitted when `Dashboard.Enabled`.
- `TestRender_NoRewrites` tightened from "no rewrite directive at all"
  to "no API-level `rewrite ^/<api>` directive" — the structural
  `rewrite ^/dashboard/...` for the dashboard mount is now legitimate.

### Migration

For installs upgrading from v0.4.0…v0.4.3:

1. `apigw upgrade` → swaps binary to v0.4.4.
2. `systemctl restart apigw-dashboard.service` → loads new HTML + JS.
3. `apigw api reload` → regenerates `/etc/nginx/sites-available/apigw.conf`
   with the v0.4.4 template (single `/dashboard/` mount).
4. Hard-refresh the dashboard in a browser. The bookmark/URL changes
   from `https://<host>/dashboard` to `https://<host>/dashboard/`
   (server-side 301 handles the legacy URL automatically).

User-defined APIs (`/api/<service>`, `/observe`, anything else) are
unaffected — they live at gateway root in their own location blocks.

## [0.4.3] — 2026-06-09

Bugfix release. Closes the ETXTBSY swap failure surfaced on the orange
pi when upgrading from v0.4.0 with the cosign-bootstrap path off.

### Fixed

- **`apigw upgrade` failed with `swap: open /usr/local/bin/apigw: text
  file busy` on most Linux installs.** `os.MkdirTemp("", ...)` puts the
  upgrade scratch dir under `$TMPDIR` (typically `/tmp` → `tmpfs`),
  while the live binary lives on rootfs. The `os.Rename` swap step
  failed with `EXDEV` (cross-device) and the historical fallback
  `copyOver` did `OpenFile(dst, O_WRONLY|O_TRUNC)` on the running
  executable — Linux returns `ETXTBSY` for that. Result: the upgrade
  finished verification, then died right before swap, leaving the host
  on the old binary.

  v0.4.3 stages the new binary as `<exe>.staging-<pid>` next to the
  live executable (same filesystem by construction), then uses a
  single atomic `rename(2)`. `rename(2)` only updates the directory
  entry — it never opens the destination, so `ETXTBSY` does not apply.
  Running processes keep their old inode alive, the new binary takes
  the path. The legacy `copyOver` helper is replaced by `copyFileTo`,
  which only writes to a brand-new path, never to a live executable.

  Tests: `internal/selfupdate/swap_test.go::TestCopyFileTo` +
  `TestCopyFileTo_OverwritesStaleStaging`.

## [0.4.2] — 2026-06-09

Follow-up to v0.4.1 — closes the cosign UX gap discovered when running
`apigw upgrade` on a host where install.sh had bootstrapped cosign into
a temp dir and discarded it. Also adds the dashboard favicon that was
missing from the published UI.

### Added

- **Dashboard favicon.** `internal/assets/dashboard/favicon.svg`
  (copied from the landing-page asset) plus a `<link rel="icon">` in
  the dashboard `index.html`. New `location = /favicon.svg` route in
  the nginx template proxies it to the dashboard daemon, in line with
  the `/app.css` + `/app.js` routes added in v0.4.1.
  `TestRender_DashboardStaticAssets` now also asserts the favicon
  route, and `_OmittedWhenDisabled` verifies it is gated on
  `Dashboard.Enabled`.

### Fixed

- **`apigw upgrade` no longer requires a host-installed cosign.**
  v0.4.0's install.sh bootstraps cosign into a temp dir for one
  verification then discards it — leaving the host without cosign on
  PATH and the first follow-up `apigw upgrade` print
  `sha256-only (cosign missing)`. `apigw upgrade` now uses the same
  bootstrap pattern as install.sh: download a sha256-pinned cosign
  (`v3.0.6`) into the upgrade tmp dir, verify against an embedded
  sha256, use it for one `verify-blob`, then discard. The pin sits in
  `internal/selfupdate/cosign_bootstrap.go` and must be bumped together
  with `scripts/install.sh` — both ship the same sha256 table.
  Exported `selfupdate.CanBootstrapCosign()` /
  `selfupdate.CosignAssetName()` / `selfupdate.PinnedCosignVersion()`
  so `apigw upgrade --check` can advertise
  `cosign+sha256 (bootstrapped v3.0.6 from sigstore)` upfront instead
  of warning about a missing cosign. Tests:
  `internal/selfupdate/cosign_bootstrap_test.go` covers the pin table
  + version-string format guards.

## [0.4.1] — 2026-06-09

Bugfix release. Surfaced during the first real-world v0.4.0 install on
a fresh Debian 13 (trixie) / arm64 host.

### Fixed

- **Dashboard CSS + JS 404 when accessed via the public TLS listener.**
  `internal/assets/dashboard/index.html` references `/app.css` and
  `/app.js` with leading slashes, but the generated nginx config only
  mounted `location /dashboard` to proxy to the dashboard daemon. The
  stylesheet and script URLs landed on `location / { return 404 }`,
  producing browser console errors —
  `Refused to apply style ... MIME type ('text/html')` and
  `Refused to execute script ... MIME type ('text/html')` — and
  rendering the dashboard as unstyled, non-interactive HTML.
  `internal/assets/nginx/_locations.tmpl` now emits two extra
  `location = /app.css` and `location = /app.js` exact-match blocks
  that proxy to the dashboard port whenever the dashboard is enabled.
  Covered by `TestRender_DashboardStaticAssets` and
  `TestRender_DashboardStaticAssets_OmittedWhenDisabled` in
  `internal/nginx/dashboard_assets_test.go`.

## [0.4.0] — 2026-06-09

Follow-up release after the v0.3.0 e2e pass. Two-session bundle: the
2026-06-08 session closed the remaining install-flow / quality bugs
surfaced during real-world testing on a clean orange-pi-class box; the
2026-06-09 session added OSS-project hygiene and a versioned admin API
surface so external consumers can pin to a stable path before the
unversioned surface is locked in.

### Fixed — install / nginx pipeline (`D-3`, `RACE-1`, `RACE-2`)

- **`D-3` — `APIGW_STATE_DIR` / `APIGW_CONFIG_DIR` / `APIGW_LOG_DIR`
  were honored inconsistently.** Some packages still hardcoded
  `/var/lib/apigw`, `/etc/apigw`, `/var/log/apigw` directly, so
  `APIGW_STATE_DIR=/tmp/foo apigw install` would silently land state in
  the system path. Refactored ~25 files to route every path lookup
  through `internal/paths`. Env vars now take effect everywhere they
  should — install, deploy, webhook, dashboard, audit, TLS, OCSP cache,
  sessions.
- **`RACE-1` — concurrent nginx mutations could interleave.** Two
  parallel `apigw api add` / dashboard CRUD calls could both call
  `WriteAndReload` concurrently, producing torn config writes when one
  process finished its template render while the other had already
  written its own. Added `sync.Mutex` to `nginx.Manager`; `Validate` /
  `Reload` / `WriteAndReload` are now serialized inside a single
  process. (Cross-process races are still prevented by the existing
  filesystem-level audit-lock.)
- **`RACE-2` — stream-include marker leaked on validate-fail.**
  `ensureStreamInclude(true)` rewrote `nginx.conf` BEFORE running
  `nginx -t`; if validation failed, the marker block stayed in
  `nginx.conf` referring to a non-existent `apigw-stream.conf`, so the
  next `systemctl restart nginx` died. `currentStreamInclude()` now
  snapshots the prior state and `revertStreamInclude(prev)` restores it
  inside the rollback path.

### Fixed — TLS / DuckDNS (`TLS-1`, `TLS-2`)

- **`TLS-1` — DuckDNS DNS-01 timeout was hardcoded.** DNS propagation
  to LE's lookup resolvers can take 5+ minutes on a busy DuckDNS shard;
  the previous fixed timeout produced spurious failures. Added
  `tls.dns_propagation_timeout_seconds` (config) — defaults preserved,
  but operators on slow shards can crank it.
- **`TLS-2` — dashboard "Renew" did not reload nginx.** The dashboard
  renew button called `StoreCert` and stopped, leaving the served cert
  stale until something else reloaded nginx. Now triggers
  `Manager.Reload()` immediately after `StoreCert`, matching CLI
  behavior.

### Fixed — deploy / webhook (`DEP-1`, `DEP-2`, `WH-1`, `WH-2`)

- **`DEP-1` — strict 2xx health check is now opt-in.** `health_path`
  field on `config.Deploy` enables a strict 2xx-probe before promoting
  a new release; absent → keeps the existing lenient TCP probe so
  deploys without a health endpoint still work.
- **`DEP-2` — build logs vanished without a Publisher.** When the
  deploy queue was driven from CLI (no SSE Hub), build stdout/stderr
  were dropped on the floor. Now `Build()` always tees through
  `os.Stderr` even when `Publisher` is nil; live log even from a
  one-off `apigw deploy run`.
- **`WH-1` — `webhook setup` printed `<your-host>` on a configured
  box.** The setup wizard used a placeholder hostname even when the
  operator had set `APIGW_PUBLIC_HOST`, configured TLS, or had a
  routable IP. Resolution order is now: `APIGW_PUBLIC_HOST` → TLS
  domain → `ServerName` (when not `_`) → routable non-loopback IP →
  `os.Hostname()` → placeholder as last resort.
- **`WH-2` — `webhook rotate-secret` had no grace window.** Pre-rotation
  webhook deliveries that arrived during the GitHub-side update were
  rejected as HMAC-invalid. Rotated secrets now keep the prior secret
  in `<deploy>.secret.prev` for a 5-minute grace window;
  `LoadValidSecrets()` returns both and `verifyAny()` accepts either,
  so GitHub's overlap period no longer drops deliveries. New unit test:
  `internal/webhook/secret_test.go`.

### Fixed — dashboard UI (`D-4` … `D-7`)

- **`D-4` — native `confirm()` dialogs replaced.** Three destructive
  actions (rollback, delete, rotate) used the browser's native
  `confirm()` — un-stylable, blocked the whole event loop, broke
  embedded views. Now an async `confirmModal({title, body, ok,
  danger})` Promise, with Esc / Enter / backdrop dismissal and
  destructive-action styling.
- **`D-5` — audit-detail `<pre>.json` was un-resizable.** Long entries
  forced the page to scroll; now `max-height: 60vh` + `resize:
  vertical` so the operator can drag the pane to fit.
- **`D-6` — expanded audit rows collapsed on every poll.** `renderAudit`
  re-rendered the whole list every 10 s, throwing away expanded-detail
  + scroll position. Now does key-based DOM-diff on `entry.id`:
  unchanged rows are kept in place, new rows insert, removed rows
  delete. Expanded detail and scroll survive.
- **`D-7` — initial dashboard load looked frozen.** Cards now apply
  `.card.loading` (dim + CSS-shimmer) while `refreshAdmin()` is in
  flight, so the operator sees something is happening.

### Added — OSS hygiene

- **`SECURITY.md`** — private vulnerability disclosure policy. GitHub
  Security Advisories as the primary channel with an email fallback;
  3-day acknowledge / 10-day initial assessment / 30-day fix target for
  High/Critical. Includes safe-harbor wording and a supported-versions
  table. Documents the cosign + sha256 + SLSA L3 supply-chain story.
- **`CONTRIBUTING.md`** — minimal contributor guide. Build / test /
  lint commands, e2e suite instructions, conventional-commit subject
  line, what kind of PR is welcome vs. what gets pushed back.
- **`gosec` job in CI** (`.github/workflows/ci.yml`). Advisory-only
  initially (`continue-on-error: true`) so a noisy false-positive can't
  wedge the release pipeline overnight; SARIF output uploaded to the
  Security tab so findings are still visible. Threshold:
  `-severity high -confidence medium`. Skips `test/e2e` + `test/benchmark`.

### Added — Dockerfile + `make docker-image`

- **`Dockerfile`** (root). Multi-stage `golang:1.25-alpine` → `alpine:3.20`,
  ~24 MB final image. Ships the static `apigw` binary plus the runtime
  prerequisites the deploy path shells out to (`git`, `curl`, `ssh`,
  `ca-certificates`). Intended as a **tool image** for CI/CD use
  (driving installs over SSH, validating YAML, exporting OpenAPI from
  a pipeline) — explicitly NOT a runtime gateway, since the gateway
  expects systemd + a real nginx on the host.
- **`make docker-image`** wraps the build with the standard
  `VERSION` / `COMMIT` / `DATE` ldflags.

### Added — admin API `/v1/` aliases (`/api/v1/admin/*`)

- Every existing `/api/admin/*` route is now ALSO mounted under
  `/api/v1/admin/*`, via a new `registerAdmin()` helper in
  `internal/dashboard/server.go`. External consumers can pin to the
  versioned surface immediately; the unversioned path remains in place
  for the embedded dashboard JS and pre-v1 CLI clients. No breaking
  changes — both prefixes resolve to the same handler.
- **`internal/dashboard/admin_v1_alias_test.go`** — regression guard.
  15 endpoint sub-tests assert that hitting `/api/v1/admin/<X>` reaches
  the same handler as `/api/admin/<X>` (identical status + body). A
  panic-guard sub-test protects the helper's invariant (path must start
  with `/api/admin`).

### Changed

- **`docs/RELEASE_QUALITY.md`** — release-quality log replacing the
  earlier commercial-readiness draft. Technical scope only: what was
  closed, what was not verified, what was added this cycle, and a
  test-gate snapshot. The earlier draft conflated tech and bizdev
  considerations that don't apply to this hobby / personal-use
  project.
- **`CONTRIBUTING.md`** wording on breaking changes — framed in terms
  of "don't break someone's working install," not "commercial users
  expect stability."

## [0.3.0] — 2026-06-08

First release driven by a comprehensive end-to-end Docker pass
(privileged systemd containers on ubuntu22 + debian12 + ubuntu24,
nginx 1.18 / 1.22 / 1.24, real `apigw install → all features →
uninstall` flows). Closes 21 issues found during that pass:

### Fixed — install / dataplane (`L-1` … `L-9`, restored from a lost working tree)

- **`L-1` — dashboard unit 226/NAMESPACE on a clean box.** The dashboard
  + webhook systemd units listed `/var/log/apigw` in `ReadWritePaths=`
  but nothing ever created it. Added `StateDirectory=apigw` +
  `LogsDirectory=apigw` and dropped both state + log paths from
  `ReadWritePaths=` so systemd owns dir creation BEFORE namespace setup.
- **`L-2` — `apigw tuning apply` could brick nginx.** On stock Debian /
  Ubuntu, `nginx.conf` already declares `worker_processes auto;` at
  main scope. `tuning apply` added a second one inside its marker
  block → `nginx -t: "worker_processes" directive is duplicate` →
  `systemctl restart nginx` would refuse the config on the next boot.
  CLI now (a) neutralises pre-existing `worker_processes /
  worker_cpu_affinity / worker_rlimit_nofile` lines with a reversible
  `# apigw-tuning-disabled:` prefix, (b) runs `nginx -t` BEFORE
  reporting `ok`, and (c) auto-restores `nginx.conf.apigw-prev` if
  validate fails. `tuning revert` undoes both the marker block AND the
  neutralisation.
- **`L-3` — `apigw install` failed on a stopped nginx.** The most
  common Debian/Ubuntu state is "nginx installed, never started";
  install used to call `nginx -s reload`, which dies with
  `invalid PID number "" in /run/nginx.pid` because there's no master
  process. `nginx.Manager.Reload` now detects this via
  `systemctl is-active --quiet nginx` and promotes to
  `systemctl start nginx` on the stopped path.
- **`L-4` — `install` left the dashboard mount serving 502.** With
  `dashboard_enabled: true` install wrote the `/dashboard` nginx mount
  but never started the unit. Now calls `system.InstallDashboardUnit`
  best-effort; prints `✓ dashboard service started on :9080` on
  success or a `Try: apigw dashboard start` hint on failure (never
  fails install).
- **`L-5` — `install --help` documented a flag that silently no-ops.**
  Example showed `--config install.yml` but the real wizard-answers
  flag is `--config-file`; `--config` is the global config-path flag
  and silently ignores wizard keys. Example now reads
  `--config-file install.yml` with a parenthetical note.
- **`L-7` — `doctor` / `status` stalled 5 s under dashboard lock.**
  When the dashboard holds the bbolt flock on `jobs.db`, both
  commands used the default 5 s timeout and printed generic
  `queue not opened`. Added `webhook.ErrQueueLocked` +
  `OpenQueueTimeout`; doctor + status open with 300 ms and report
  `✓ queue-depth busy — held by running dashboard`. Doctor + status
  now finish in ~300 ms under a held lock.
- **`L-8` — `status` painted intentional inactivity as red ×.** The
  in-process webhook (served by the dashboard daemon on `:9000`)
  rendered as `× apigw-webhook.service inactive`; the
  `tls-renew.timer` did the same after `--no-timer`. Now renders both
  as neutral `•` with `served by dashboard` / `disabled` labels.
- **`L-9` — `consumer add <id>` left NAME blank.** Without `--name`,
  `consumer list` showed `-` under NAME. Now defaults `Name` to `id`.

### Fixed — admin API / dashboard (`D-1` / `D-2a` / `D-2b` / `D-8`)

- **`D-1` — JWT verifier re-fetched JWKS on every `auth_request`.**
  Each request rebuilt the `auth.Verifier`, which restarted JWKS
  fetching from scratch — adding hundreds of ms latency to every
  JWT-protected route and hammering (likely getting rate-limited by)
  Auth0 / Okta / Keycloak. Added a process-level cache keyed by
  `(apiName, configHash)`; verifier survives across requests, JWKS
  cache survives with it, hash invalidates on any JWT config change.
- **`D-2a` — OIDC SSO had no UI entry point.** An operator with
  `security.sso` configured had to know to hand-type
  `/api/admin/sso/login`. `/api/status` now exposes `sso_enabled`;
  `index.html` carries a hidden `<a id="sso-btn" …>Sign in with SSO</a>`
  link that `app.js` unhides when `sso_enabled` is true.
- **`D-2b` — `Security.Identify()` ignored the session cookie.**
  After a successful SSO callback the cookie was set but the admin API
  still treated the operator as `anonymous` — every panel stayed
  "Sign in to view…", every write 403'd. `Security.SessionResolver`
  hook + `dash.SessionIdentity` wire the resolver: `Identify` now
  falls back to `apigw_session` via `SessionStore` when no
  `Authorization` / `X-Apigw-Subject` is present.
- **`D-8` — `apigw dashboard serve` was silent at default WARN.** A
  one-line `apigw dashboard listening on :addr (webhook on :addr)`
  banner now prints to stderr at startup regardless of log level so
  manual runs aren't empty.

### Fixed — comprehensive Docker pass (`A1` … `A7` + papercuts)

- **`A1` — `apigw api log set --format json` made `nginx -t` fail.**
  The per-API `AccessLog` mode emitted `access_log … apigw_json;` in
  the server block but the `log_format apigw_json` directive in
  `apigw-http.conf` was gated solely on `cfg.Logging.Format == "json"`
  → `nginx -t: unknown log format "apigw_json"`. Generator now folds
  per-API `AccessLog` into the http-scope `log_format` gate.
- **`A4` — two `auth_request` directives at the same location.**
  Configuring two of `JWT / MTLS / APIKey / HMAC / OAuth2 / Session`
  on one API emitted two `auth_request` directives in `_locations.tmpl`
  → `nginx -t: "auth_request" directive is duplicate`. Generator now
  picks exactly one method per route, by documented priority:
  **JWT > MTLS > APIKey > HMAC > OAuth2 > Session**. Config stays
  whole; lower-priority methods are silently skipped at render time.
- **`A5` — `MemoryDenyWriteExecute` was silently OFF.** The systemd
  unit templates wrote `MemoryDenyWriteExecute=true        # safe: Go
  has no JIT`; systemd parses the trailing comment as part of the
  boolean value, fails to parse it, and silently ignores the
  directive. MDWX (a Go-safe hardening directive) was therefore OFF on
  every install since the template was last edited. Comment moved
  above the directive, with a warning to keep it there.
- **`A6` — `apigw stream add` was dead code.** CLI reported
  `✓ added tcp stream redis on :6380 → …` but never wrote
  `apigw-stream.conf` and never injected `stream { include … }` into
  `nginx.conf`. Stream conf now lives at `/etc/nginx/apigw-stream.conf`
  (outside `conf.d/` — conf.d is auto-included inside `http{}` and
  stream{} cannot sit there), `Manager.WriteAndReload` renders +
  writes it alongside the server + http files, and
  `ensureStreamInclude(want bool)` adds / removes the marker block in
  `nginx.conf` idempotently. `stream add / remove` invoke
  `WriteAndReload` directly so the dataplane catches up immediately.
- **`A7` — mTLS handler returned 500 instead of 403.** Dashboard
  `handleMTLSAuth` returned `MTLSBackendError` (HTTP 500) when nginx
  forwarded an unparseable or absent client-cert PEM, painting "Server
  Error" in front of an auth failure. `VerifyMTLS` now maps "no cert
  body + no DN forwarded" and "parse cert failed" to `MTLSNotAllowed`
  (HTTP 403), with the reason in the structured log.
- **`audit-lock` — `apigw audit *` stalled when the dashboard held
  the lock.** Same shape as `L-7`. Added `audit.ErrAuditLocked` +
  `audit.OpenTimeout`; CLI opens with 300 ms and prints
  `× audit locked by another apigw process … use /api/admin/audit
  endpoint via the dashboard instead`.
- **`tls-renew` — positional domain arg was rejected.** `apigw tls
  renew apigw.local` failed with `unknown command "apigw.local"`. The
  command now takes an optional domain filter; unknown domain returns
  a specific error instead of cobra's "unknown command".
- **`uninstall` — left stale apigw conf behind.** `apigw uninstall`
  removed `sites-available/apigw.conf` + the enabled-link but left
  `conf.d/apigw-http.conf`, `/etc/nginx/apigw-stream.conf`, and the
  tuning marker block in `nginx.conf`. Now also removes both conf
  files (+ their `.apigw-prev` backups) and calls `tuning.Revert()`
  so a `nginx -t` post-uninstall reflects the stock nginx.conf the
  operator started with.

### Fixed — deploy nginx render

- **`deployEntry` was missing 26 mirror fields from `apiEntry`.** The
  shared `_locations.tmpl` reads the full apiEntry surface; without
  the mirrors, every deploy render panicked with
  `can't evaluate field LifecycleDraft in type nginx.deployEntry` →
  `apigw migrate`, `apigw dashboard start`, and `webhook` payload
  delivery all broke. Added zero-valued mirrors so deploys fall
  through to plain proxy.

### Fixed — e2e harness

- **Dockerfiles missing `python3` + `netcat-traditional` and
  `/var/log/nginx`.** The test backend on `:3000` couldn't start and
  `nginx -t` failed without the log dir. All three e2e Dockerfiles
  (`debian12`, `ubuntu22`, `ubuntu24`) now install both packages and
  recreate `/var/log/nginx`.
- **`testAPI` flaked on the nginx graceful-reload window.** Added
  `harness.HTTPGetUntil` (polls until the expected code or timeout)
  so the `api add → curl` race doesn't fail intermittently.
- **`TestScenarios` gained a `dashboard_service` step** that runs
  `apigw dashboard start` so L-1 doesn't regress unnoticed (the prior
  suite never exercised the dashboard unit).
- **`TestWebhook` secret-extract regex** was matching a numbered
  bullet in the help text and printing `"4."` as the "secret". Now
  greps a unique line (`copy it now`) to find the real secret.

### Internal

- `paths.NginxConfDir()` helper for files that must live OUTSIDE
  `conf.d/` (stream conf today; future main-context blocks).

### Test coverage

| Surface | Status |
|---|---|
| `go test -race -count=1 ./...` (28 packages) | ✅ |
| `go vet ./...` + `gofmt -l internal cmd test` | ✅ |
| `make e2e DISTRO=ubuntu22` (TestScenarios + TestMigrate + TestWebhook) | ✅ |
| `make e2e DISTRO=debian12` (same) | ✅ |
| `make e2e DISTRO=ubuntu24` (same) | ✅ |
| Hand-driven 13-flow walkthrough (install → … → uninstall) | ✅ |
| Comprehensive 22-CLI-group + admin API + routing render pass | ✅ |

Full breakdown: see `docs/RELEASE_TEST_REPORT.md`.

### Security
- **Admin API hardened.** `/api/admin/*` endpoints now go through a single
  `Security.Guard()` gate: bearer-token authentication, CSRF check,
  RBAC permission, OPA policy evaluation, optional N-of-M approvals,
  and a hash-chained audit log. Legacy installs without a `security:`
  block still work in unauthenticated mode for backwards compatibility.
- **TLS private-key snapshots** are now forced to `0o600` regardless of
  the source file's mode (previously `snapshotExisting` inherited
  whatever permission the live file had, so a world-readable privkey
  rolled back to a world-readable backup).
- **JWKS verifier** now caps redirect chains at 2 (stdlib's 10-hop
  default let a slow/malicious IdP stall every `auth_request` call).
- **Install shim** now requires `APIGW_ACK_UNSAFE=yes` alongside
  `APIGW_SKIP_COSIGN=1`, strips proxy env vars before any `curl`, and
  pauses 3s with a banner so a sleepwalking operator notices.
- **nginx config files** drop from `0o644` to `0o640` (defence in
  depth; the file may name auth endpoints and upstreams).
- **Build log scrubber.** Every byte streamed to journald/SSE from a
  build step now passes through a heuristic scrubber that masks
  `KEY=VALUE`, `Authorization:`, and `://user:pass@` patterns.
- **Webhook rate limit.** 100 deliveries per (deploy, minute) by
  default; throttled requests get `429 Retry-After: 60`.
- **CSRF protection** on `/api/admin/*` writes via HMAC-signed tokens
  issued at `/api/admin/csrf`.

### Added
- **RBAC.** Built-in `viewer / operator / admin / owner` roles plus
  YAML-defined custom roles; permission strings support `*` and
  `deploy.*` wildcards. Defaults to soft-rollout mode (log denials,
  don't refuse) until `security.rbac_enforce: true` flips it.
- **Audit log.** bbolt-backed, hash-chained Entries
  (`/var/lib/apigw/audit.db`); SIEM-friendly JSON-lines export via
  `/api/admin/audit` and tamper-detection via `VerifyChain()`.
- **OPA policies.** Drop `*.rego` files in `security.policy_dir` and
  every config mutation runs through them. Empty deny set = allowed.
- **Approvals.** Mark a mutation as `Dangerous` and (when
  `approvals_threshold > 0`) it parks until N other operators approve
  via `/api/admin/approvals/{id}/approve`. bbolt-backed, 24 h TTL.
- **Tenants.** Multi-tenancy primitives wired into the admin API:
  per-tenant path prefixes, MaxAPIs/MaxDeploys quotas, tenant-admin
  isolation; visible at `/api/admin/tenants`.
- **Alerts dispatcher.** Slack / Microsoft Teams / PagerDuty / generic
  webhook / SMTP notifiers fire on `cert.expiring`, `cert.expired`,
  `deploy.failed`, `gitops.applied`, and operator-test events. Test
  via `POST /api/admin/alerts/test`.
- **GitOps reconciler.** Set `gitops.repo_url` and the dashboard
  daemon clones it on `gitops.interval_sec` (default 30 s) and copies
  `<path>/config.yaml` onto `/etc/apigw/config.yaml` via atomic
  rename. Each reconcile fires an alert and is audit-logged.
- **Optional Raft cluster.** Set `cluster.node_id` + `cluster.peers`
  to replicate `config.yaml` across peers. Leader-only writes, all
  peers serve reads; per-host state (release dirs, secrets) stays
  local.
- **Operator endpoints:** `/api/admin/audit`, `/api/admin/approvals`,
  `/api/admin/tenants`, `/api/admin/alerts/test`, `/api/admin/csrf`.

### Fixed
- **`internal/dashboard/server.go:248`** — nil-pointer crash when
  Metrics is nil and an SSE client connects.
- **`internal/deploy/jobs.go:61`** — `Submit()` no longer overrides
  the caller's context with a hidden 30-minute timeout.
- **`internal/webhook/worker.go`** — silent `_ = cfg.Save()` /
  `SetDeployStatus()` failures are now logged via slog instead of
  vanishing.
- **`internal/deploy/clone.go`** — concurrent clones of the same SHA
  now succeed both (idempotent) instead of one failing on `Rename`.
- **`internal/dashboard/server.go` `writeJSON`** — encoder errors are
  logged instead of swallowed.

### Removed
- Stale Node.js / "React-like UI" entries from the changelog. The
  project is a single static Go binary; the dashboard is vanilla JS
  embedded via `go:embed`.

## 2025-2026

The `apigw` Go binary replaced the bash `install.sh` +
`api-manage` flow. See `git log` for granular history.
