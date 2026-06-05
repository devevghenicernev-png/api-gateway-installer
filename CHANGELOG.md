# Changelog

All notable changes to `apigw` are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/), versions follow SemVer.

## [Unreleased]

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
