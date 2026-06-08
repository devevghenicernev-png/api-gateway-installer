# Changelog

All notable changes to `apigw` are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/), versions follow SemVer.

## [Unreleased]

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
