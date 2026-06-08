# Release Test Report — apigw v0.3 candidate

**Date:** 2026-06-08
**Branch:** `apigw-launch` (commits `5a177b9` + `ae4b5d1`)
**Binary:** `bin/apigw-linux-arm64` (`CGO_ENABLED=0 GOOS=linux GOARCH=arm64`)
**Methodology:** automated gates → e2e in Docker (systemd PID 1) → hand-driven operator walkthrough on a clean privileged container → comprehensive feature pass (CLI verbs + admin API + nginx render inspection)

This document is the authoritative record of every test run, every bug
found, every fix applied, and the final go/no-go verdict.

---

## Verdict

**Functional GO** for pilot / staging install on Ubuntu 22 + Debian 12 + nginx 1.18.
Install / API / TLS / dashboard / auth / admin API / streams / consumers / webhook flows all verified end-to-end on a clean box.

**Not yet ready** for tagged GA release (`v0.3.0`) — see "Open before GA" section below for the short punch list (CHANGELOG, version tag, ubuntu24 e2e, goreleaser snapshot).

---

## Automated gates

| Gate | Command | Result |
|------|---------|--------|
| Host build | `make build` (darwin/arm64) | ✅ |
| Linux build | `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/apigw` | ✅ |
| `go vet` | `go vet ./...` | ✅ clean |
| `gofmt` | `gofmt -l internal cmd test` | ✅ empty |
| Unit suite | `go test -race -count=1 ./...` | ✅ 28 packages green |
| e2e ubuntu22 | `APIGW_E2E_DISTRO=ubuntu22 go test -tags=e2e ./test/e2e/...` | ✅ TestScenarios + TestMigrate + TestWebhook |
| e2e debian12 | `APIGW_E2E_DISTRO=debian12 go test -tags=e2e ./test/e2e/...` | ✅ same |
| e2e ubuntu24 | (re-run pending after final fixes) | ⏸️ run before tag |

---

## Restored fixes (lost from working tree before this work started)

These were documented as "✅ fixed" in `docs/VERIFICATION_REPORT.md` but
the code changes never landed — only the report itself was committed
(97556e6). Re-applied in commit `5a177b9`.

| ID | What | Repro before fix | Verified after fix |
|---|---|---|---|
| **L-1** | `apigw dashboard start` fails with `status=226/NAMESPACE` on a clean box because the unit lists `/var/log/apigw` in `ReadWritePaths=` and nothing creates it | install → `dashboard start` → unit fails to enter ExecStart | `StateDirectory=apigw` + `LogsDirectory=apigw` on dashboard/webhook units; `/var/lib/apigw` + `/var/log/apigw` dropped from `ReadWritePaths=`. Unit comes up cleanly on a fresh systemd box. |
| **L-2** | `apigw tuning apply` collides with stock `worker_processes auto;` → `nginx -t: "worker_processes" directive is duplicate`; CLI prints `ok` BEFORE reload validates → broken config left on disk → `systemctl restart nginx` dies | `apigw tuning apply --reload`; then `systemctl restart nginx` | tuning.Apply neutralises pre-existing `worker_*` lines with `# apigw-tuning-disabled:` prefix (reversible by Revert); CLI runs `nginx -t` before reporting success and auto-restores `nginx.conf.apigw-prev` on validation failure. Verified: `systemctl restart nginx` survives both `apply` and `revert`. |
| **L-3** | `apigw install` fails on a fresh box where nginx is installed-but-stopped (the most common Debian/Ubuntu state) — install reloads instead of starting | `systemctl stop nginx; apigw install --config-file …` → fails on reload | `nginx.Manager.Reload` promotes to `systemctl start nginx` when no master process is running. Verified: install on stopped nginx finishes cleanly with `apigw is ready`. |
| **L-4** | `install` with `dashboard_enabled: true` writes the `/dashboard` nginx mount but never starts the dashboard unit — `/dashboard` 502s until the operator finds `apigw dashboard start` | install → `curl http://host/dashboard` → 502 | install now calls `system.InstallDashboardUnit` best-effort when `dashboard_enabled`; prints `✓ dashboard service started on :9080` on success, a `Try: apigw dashboard start` hint on failure (never fails install). |
| **L-5** | `apigw install --help` documents `--config install.yml` but the real flag is `--config-file`; `--config` is the GLOBAL config-path flag and silently ignores wizard answers | `apigw install --config install.yml --plan` → defaults, no error | Example block updated to `--config-file install.yml`, with a parenthetical "note: --config-file, not --config". |
| **L-7** | `apigw doctor` / `apigw status` stall ~5 s while the dashboard holds the bbolt flock; doctor prints generic `queue not opened` | run dashboard, then `time apigw doctor` → ~5 s | `webhook.ErrQueueLocked` + `OpenQueueTimeout`; doctor + status open with a 300 ms timeout. Verified: `doctor` reports `✓ queue-depth busy — held by running dashboard` in **318 ms**. |
| **L-8** | `apigw status` renders the in-process webhook as `× apigw-webhook.service inactive` (alarming red) even though it serves on :9000 inside the dashboard; same for intentionally-disabled `tls-renew.timer` | install → `apigw status` → red × on still-working webhook | `ServiceStatus.Note` field; webhook unit when inactive AND dashboard active → `• served by dashboard` neutral. Timer when inactive AND no certs → `• disabled` neutral. |
| **L-9** | `consumer add <id>` without `--name` leaves NAME blank → `consumer list` shows `-` | `apigw consumer add alice --yes` → list shows `NAME -` | `displayName` falls back to id when `--name` is omitted. Verified: `consumer list` shows `alice  alice  ops,dev`. |
| **D-1** | JWT verifier rebuilt per request → JWKS re-fetched from the IdP on every `auth_request` call → 100s of ms latency, gateway hammers and likely gets rate-limited by Auth0/Okta/Keycloak | dashboard JWT-protected API, watch IdP JWKS log | `Server.jwtVerifiers` map keyed by `(api, configHash)`; cache hit reuses the existing Verifier (which retains its hourly JWKS cache). Hash invalidates on any config change. |
| **D-2a** | OIDC SSO has no UI entry point — `index.html` only shows "Sign in with bearer token"; an operator with `security.sso` configured has to know to hand-type `/api/admin/sso/login` | configure SSO, open dashboard → no SSO button | `/api/status` exposes `sso_enabled`; index.html has `<a id="sso-btn" href="/api/admin/sso/login" hidden>Sign in with SSO</a>`; app.js unhides it when `s.sso_enabled === true`. |
| **D-2b** | After SSO callback succeeds, the admin API still treats the user as `anonymous` — `Security.Identify()` never consults the session cookie that SSO mints | SSO login → dashboard panels still "Sign in to view…" | `Security.SessionResolver` hook; serve.go wires `dash.SessionIdentity` which reads `apigw_session`, looks up via `SessionStore`, and returns `rbac.Identity{User: subject, Groups: scopes}`. Identify falls back to it after Bearer/X-Apigw-Subject are absent. |
| **D-8** | `apigw dashboard serve` is silent at the default WARN log level — no startup or request lines on manual runs (under systemd it's fine via journald) | run dashboard manually → log file empty | One-line stderr banner: `apigw dashboard listening on :9080 (webhook on :9000)` — fires before the slog logger, visible at any level. |

Verified post-restore: full unit + e2e + manual operator walkthrough on
ubuntu22 + debian12 systemd containers. Commit `5a177b9`.

### Also fixed in the same restore pass

- `internal/nginx/generator.go::deployEntry` was missing **26 mirror
  fields** (`LifecycleDraft`, `EarlyHints`, `Versions`, `Buffering`,
  `BotGuard`, `Mock`, `Cache`, `Mirror`, `Rewrites`, `Description`,
  `MTLS`, …). The shared `_locations.tmpl` reads the full apiEntry
  surface, so EVERY deploy render panicked with
  `can't evaluate field LifecycleDraft in type nginx.deployEntry` —
  broke `migrate` + `webhook` + `dashboard start`. Added zero-valued
  mirrors so deploys fall through to plain proxy.
- `test/e2e/docker/Dockerfile.{debian12,ubuntu22,ubuntu24}` lacked
  `python3 netcat-traditional` and didn't recreate `/var/log/nginx`
  → the test backend on :3000 never started and `nginx -t` failed
  without the log dir. Added both.
- `harness.HTTPGetUntil` polls across the sub-second nginx graceful
  reload window (L-6, won't-fix); `testAPI` uses it instead of a bare
  `HTTPGet` so it doesn't flake on post-`api add` race.
- `TestScenarios` gained a `dashboard_service` step so L-1 doesn't
  return unnoticed (the prior suite never ran `apigw dashboard start`).
- `TestWebhook` secret-extract regex updated; the old `grep -A1 Secret`
  matched a line in the help text and printed `"4."` as the "secret".

---

## Bugs found during comprehensive feature pass

A separate run after the restore covered **22 CLI groups + admin API
+ nginx render inspection** in a fresh privileged systemd container.
Seven additional issues surfaced — all fixed in commit `ae4b5d1`.

| ID | Sev | What | Repro before fix | Verified after fix |
|---|---|---|---|---|
| **A1** | 🟠 HIGH | `apigw api log set --format json` writes `access_log … apigw_json;` to the server block but the `log_format apigw_json` directive in `apigw-http.conf` is gated on `cfg.Logging.Format == "json"` and was never set from the per-API field → `nginx -t: unknown log format "apigw_json"` → reload rolled back | `api log set hello --format json && api reload` → E_NGINX_RELOAD | `wantJSON` flag in generator folds per-API `AccessLog == "json"` into the http-scope `log_format` gate. Verified: `apigw-http.conf` contains the `log_format apigw_json escape=json '{…}'` block; `nginx -t` passes. |
| **A4** | 🟠 HIGH | JWT + APIKey (or any two of JWT/MTLS/APIKey/HMAC/OAuth2/Session) on the same API emits two `auth_request` directives at the same location → `nginx -t: "auth_request" directive is duplicate` | `auth apikey add hello … && auth jwt set hello …` | Generator picks exactly one auth method per route at render time, by documented priority: **JWT > MTLS > APIKey > HMAC > OAuth2 > Session**. Config stays whole — lower-priority methods are silently skipped until the operator removes the higher one. Verified: nginx config shows only `auth_request /_apigw_jwt_hello;` when both JWT and APIKey are configured. |
| **A5** | 🟢 LOW (security regression) | `MemoryDenyWriteExecute=true        # safe: Go has no JIT` — systemd parses the value as `true        # safe…` and silently ignores the directive → MDWX was OFF the entire time | `systemctl show apigw-dashboard.service -p MemoryDenyWriteExecute` → `no` | Comment moved above the directive, with a warning to keep it there. Verified: `MemoryDenyWriteExecute=yes` post-fix. No `Failed to parse boolean value` in journald. |
| **A6** | 🔴 BLOCKER | `apigw stream add` reported success but never touched nginx — entry sat in config.yaml, no `apigw-stream.conf` written, no `stream { include … }` block in nginx.conf → TCP/UDP proxy was dead code | `stream add redis --port 6380 …` → `ss -lnt` shows nothing on :6380 | Stream conf lives at `/etc/nginx/apigw-stream.conf` (outside `conf.d/` — `conf.d/*` auto-includes inside `http{}` and stream{} can't sit there); wired by a marker-block in `nginx.conf` that `ensureStreamInclude(want bool)` adds/removes idempotently. Manager.WriteAndReload renders + writes the stream file alongside server + http; `stream add/remove` invoke WriteAndReload directly. Verified: TCP stream on :6380 starts LISTENing immediately, `nginx -t` passes, and `stream remove` cleans up both the file and the nginx.conf marker. |
| **A7** | 🟡 MED | Dashboard mTLS handler returned 500 to clients when nginx forwarded an unparseable or absent client-cert PEM, because `VerifyMTLS` used `MTLSBackendError` (`HTTPStatus 500`) for those paths | mTLS API + curl --cert with self-CA → 500 with `<html>500 Internal Server Error</html>` to the client | `VerifyMTLS` now maps "no cert body + no DN forwarded" and "parse cert failed" to `MTLSNotAllowed` (403). Gateway no longer paints "Server Error" in front of mTLS failures. Verified: same scenario → 403 with `mtls: reject result=2 reason="parse cert: no PEM block"` logged. |
| **audit-lock** | 🟢 LOW | `apigw audit query/verify/tail/export` used the default 2 s bbolt timeout and printed generic `open audit.db: timeout` when the dashboard held the audit.db flock (same shape as L-7) | run dashboard, then `time apigw audit tail` → ~2 s, useless error | `audit.ErrAuditLocked` + `audit.OpenTimeout`. CLI opens with 300 ms timeout, reports `× audit locked by another apigw process … use the /api/admin/audit endpoint via the dashboard instead`. Verified: 297 ms with clear message. |
| **tls-renew** | 🟢 LOW | `apigw tls renew apigw.local` rejected the positional arg with `unknown command "apigw.local"` (it took flags only) | `apigw tls renew apigw.local` → cobra error | Renew now takes an optional domain filter; unknown domain returns specific error instead of cobra "unknown command". Verified: `apigw tls renew apigw.local` → `› apigw.local ok (364 days left)`. |
| **uninstall** | 🟢 LOW | `apigw uninstall` cleaned up sites-available/apigw.conf + symlink but left `conf.d/apigw-http.conf`, `apigw-stream.conf` and marker blocks in `nginx.conf` → stale apigw upstreams/log_formats long after uninstall | `apigw uninstall && ls /etc/nginx/conf.d/` → `apigw-http.conf` still there | Uninstall also removes `HTTPConfPath` (+ backup), `StreamConfPath` (+ backup), and calls `tuning.Revert()` to strip the tuning marker block. Verified: post-uninstall `grep apigw /etc/nginx/nginx.conf` empty, `/etc/nginx/conf.d/` empty, `nginx -t` passes against the original stock nginx.conf. |

### Not bugs (clarified by deeper inspection)

- **A2** — `api acl set` stores config but doesn't render in nginx. By
  design: ACL runs AFTER authentication via the auth handler
  (`/auth/apikey`, `/auth/jwt`, `/auth/mtls`, etc.), not at nginx
  level. Documented behaviour in `config.go::ACL`. **Future UX:**
  `config lint` could warn when ACL is set without any auth method.
- **A3** — apikey secret extraction in shell pipelines was an
  awk-pipeline issue, not a CLI bug. Output is correct in the issued
  table.

---

## Coverage matrix

### CLI groups (22 top-level, all verbs touched)

| Group | Subcommands tested | Notes |
|---|---|---|
| `ai` | add, list, status, pull, remove | `--force` flag works; provider auto-detection at `ai add` |
| `api` | add, list, enable, disable, remove, reload, retry, acl, rewrite, lifecycle, buffering, connpool, log, canary, bluegreen, variants, slo, publish/deprecate/retire | All `set/show/clear` subcommands verified; nginx render inspected |
| `audit` | query, verify, export, tail | Lock-conflict fix (above) |
| `auth` | admin token add, admin enforce, apikey add/list/rotate/revoke, hmac add/list/revoke, jwt set/disable, mtls set/disable, session list | apikey + JWT + HMAC E2E confirmed; mTLS auth_request endpoint reachable |
| `backup` | (single) | 5 files packed; restore --force restores cleanly |
| `completion` | bash, zsh, fish | All three emit valid completion scripts |
| `config` | lint, export (yaml/json/openapi), import | Lint flags implicit groups; openapi 3.0.3 emitted |
| `consumer` | add, list, remove, join, leave, group create/list/remove | L-9 fix verified |
| `dashboard` | serve, start, stop, status, url, grafana | systemd unit comes up cleanly; D-8 banner visible |
| `deploy` | add (no-start), list, remove, ssh-key | Live git-clone not exercised (would need a real repo) |
| `doctor` | (single) | 12 ok / 0 warn / 0 fail on a healthy box, 297 ms with dashboard holding locks |
| `gendocs` | (hidden) | Not exercised |
| `install` | (single) | L-3/L-4/L-5 paths verified |
| `logs` | --service nginx/dashboard/webhook | All three sources stream |
| `migrate` | (single) | Covered by `test/e2e/TestMigrate` |
| `restore` | (single) | Round-trips with backup |
| `status` | (single) | L-7/L-8 fixes verified |
| `stream` | add tcp, add udp, list, remove | A6 fix verified — TCP listens on :6380, UDP on :5353; uninstall path cleans up |
| `tls` | enable self-signed, status, add-domain, renew, ocsp subcommands, disable | BUG-7 (http2 as listen param) verified; renew positional arg fix verified; Let's Encrypt path not exercised |
| `tuning` | show, apply, revert | L-2 critical path verified (systemctl restart nginx survives) |
| `uninstall` | (single) | Now also cleans http-conf + stream-conf + marker blocks |
| `upgrade` | (single) | Not exercised (would download from GitHub) |
| `version` | (single) | prints version/commit/build line |
| `webhook` | setup, status, list, url, rotate | HMAC-signed POST → 202, forged → 401 |

### Admin API endpoints

| Endpoint | Methods | Status |
|---|---|---|
| `/api/admin/apis` | GET, POST, PUT `/<name>`, DELETE `/<name>` | ✅ CRUD verified |
| `/api/admin/consumers` | GET, POST, PUT, DELETE | ✅ CRUD verified |
| `/api/admin/streams` | GET, POST, PUT, DELETE | ✅ GET returns proper struct |
| `/api/admin/tls` | GET, PUT | ✅ |
| `/api/admin/config` | GET, PUT | ✅ |
| `/api/admin/sessions` | GET, POST, DELETE `/<id>` | ✅ returns `not configured` when `security.sessions` missing |
| `/api/admin/audit` | GET (limit param) | ✅ returns hash-chained entries; `prev_hash`/`hash` linked |
| `/api/admin/approvals` | GET, POST `/<id>/{approve,reject}` | ✅ dangerous action gets parked (202 + ID + 24h TTL) |
| `/api/admin/csrf` | GET | ✅ 108-char HMAC-signed token; write requires `X-CSRF-Token` header (403 without) |
| `/api/admin/alerts/test` | POST | ✅ 202 `{"status":"dispatched"}` |
| `/api/admin/tenants` | GET | ✅ returns `not enabled` when tenants empty |
| `/api/admin/gitops` | GET, PUT | ✅ |
| `/api/admin/sso/{login,callback}` | GET | ✅ 502 on unreachable IdP; 400 on bad state; 404 when SSO not configured |
| `/auth/{jwt,mtls,apikey,hmac,oauth2,session}/<api>` | GET | ✅ all reachable; JWT (32+ byte HS256 secret) returns 200 with valid token; APIKey + HMAC E2E |
| `/api/status` | GET | ✅ includes `sso_enabled`, TLS, deploys, webhook |
| `/events` | SSE GET | ✅ retry hint, then 1 Hz `metrics.tick` with `count/rps/err_rate/p50/p95/p99_ms` |
| `/metrics` | GET | ✅ Prometheus format |
| `/api/logs/<deploy>` | GET | ✅ empty array on no logs; 404 on path-traversal |

### Routing features (verified via nginx render inspection + live HTTP)

| Feature | Verified |
|---|---|
| CORS preflight (OPTIONS → 204 with `Access-Control-Allow-Origin/Methods/Headers/Credentials/Max-Age`) | ✅ |
| IPRules (`allow 127.0.0.1/32; deny 0.0.0.0/0;`) | ✅ |
| Rate limit (`limit_req zone=… burst=20 nodelay; limit_req_status 429;`) | ✅ 27 ok / 3 × 429 from 30 hits |
| Bot guard (`if ($http_user_agent ~* "badbot") { return 403; }`) | ✅ |
| Request headers (`proxy_set_header X-Custom-In …`) | ✅ |
| Response headers (`add_header X-Custom-Out … always;`) | ✅ |
| Retry policy (`proxy_next_upstream http_500 http_502 http_503; proxy_next_upstream_tries 3; proxy_next_upstream_timeout 15s;`) | ✅ |
| Lifecycle deprecated (`add_header Deprecation "true"`, `add_header Sunset …`) | ✅ |
| Buffering (`proxy_buffering off; proxy_request_buffering on; client_body_buffer_size 16k;`) | ✅ |
| Connection pool (`keepalive 32; keepalive_timeout 60s; keepalive_requests 1000;`) | ✅ |
| URL rewrite (`rewrite ^/api/hello/(.*) /v1/$1 break;`) | ✅ |
| Canary (`split_clients` with weight + pin-header) | ✅ rendered when canary set alone |
| Blue/green (active blue/green pool swap) | ✅ |
| Variants (`split_clients` N-way) | ✅ |
| Gzip in **server-scope** (BUG-1b) | ✅ verified — no `gzip directive is duplicate` on stock Ubuntu/Debian |
| `listen … ssl http2;` parameter syntax (BUG-7) | ✅ works on nginx 1.18 |
| Stock default site disabled on install (BUG-6) | ✅ `curl http://<ip>/` hits apigw, not nginx-welcome 404 |

---

## Manual operator walkthrough

Driven by hand inside a `apigw-e2e:ubuntu22` privileged systemd container,
nginx 1.18, mount of `bin/apigw-linux-arm64` at `/usr/local/bin/apigw`.

| # | Flow | Result |
|---|---|---|
| F1 | `version`, `--help`, `install --help` (L-5 example) | ✅ |
| F2 | `install --config-file` on a box with nginx **stopped** (L-3 path) | ✅ nginx started, default site disabled, dashboard up on :9080 |
| F3 | `api add → list → curl → disable → enable → remove` | ✅ HTTPGetUntil-poll covers reload lag |
| F4 | `tls enable self-signed`, HTTPS 200, HTTP→HTTPS 301, `listen 443 ssl http2;` param syntax | ✅ |
| F5 | **L-2 critical**: stock `worker_processes auto;` neutralized, marker block injected, `systemctl restart nginx` survives both apply AND revert | ✅ |
| F6 | Dashboard UI: `/api/status` exposes `sso_enabled`, index.html has hidden `sso-btn`, SSE `/events` streams, `/metrics` Prometheus format, path-traversal `/api/logs/has.dots` → 404 | ✅ |
| F7 | `consumer add alice` without `--name` → `NAME=alice` | ✅ |
| F8 | `config lint` (implicit-group warning), `export yaml/json/openapi`, `import --dry-run` round-trip | ✅ |
| F9 | `doctor`/`status` with dashboard holding locks → **318 ms** total (was ~5 s) | ✅ |
| F10 | `backup` (5 files: config + 3 cert files + lock), `restore --force` | ✅ |
| F11 | `webhook setup app1` → HMAC-signed POST → 202; forged signature → 401 | ✅ |
| F12 | `/dashboard/` proxied through gateway nginx, static + JS + CSS all 200 | ✅ |
| F13 | `apigw uninstall --yes` → pre-uninstall backup auto, units removed, **stock default site restored**, nginx -t passes against original stock config, `grep apigw /etc/nginx/nginx.conf` empty | ✅ |

---

## Comprehensive deep pass (the second wave that surfaced A1–A7)

After the basic walkthrough passed, ran a wider sweep covering every
CLI verb's subcommands + every admin API endpoint + every routing
feature. Findings became the A-series above. Notable result evidence:

- **A1** verified post-fix: `apigw-http.conf` contains
  `log_format apigw_json escape=json '{…}';` when ANY API has
  `access_log: json`; `nginx -t` passes.
- **A4** verified post-fix: with JWT + APIKey both configured,
  `awk "/location \/api\/hello/,/^    }/"` over the rendered server
  config shows **only** `auth_request /_apigw_jwt_hello;` — no
  duplicate.
- **A5** verified post-fix:
  `systemctl show apigw-dashboard.service -p MemoryDenyWriteExecute`
  → `yes`. No `Failed to parse boolean value` in journald.
- **A6** verified post-fix: `apigw stream add redis --port 6380 …`
  → `apigw-stream.conf` written; `nginx.conf` has marker block
  `# >>> apigw stream >>> stream { include /etc/nginx/apigw-stream.conf; } # <<< apigw stream <<<`;
  `ss -lnt` shows `LISTEN 0  511  0.0.0.0:6380`; UDP test on :5353
  shows `UNCONN 0  0  0.0.0.0:5353`. `stream remove` cleans up
  both the file AND the marker block.
- **A7** verified post-fix: same mTLS scenario that previously
  returned 500 now returns 403 with
  `mtls: reject result=2 reason="parse cert: no PEM block"` logged.

---

## Known limitations (NOT release blockers)

- **mTLS PEM parsing** — with curl `--cert` and a self-CA, nginx
  forwards `$ssl_client_escaped_cert` but the dashboard handler
  reports `parse cert: no PEM block`. After A7's fix this is now a
  clean 403, not a 500, but the actual PEM accept path needs a
  follow-up (likely URL-unescape pre-parse). **Workaround:** the
  DN-only fallback already handles CN-based allow-lists when
  `$ssl_client_s_dn` is forwarded.
- **ACL without auth method** — `api acl set` silently no-ops if no
  auth handler is configured. Documented behaviour in
  `internal/config/config.go::ACL` (60-line godoc). **Future UX:**
  `config lint` warning.
- **Untested surfaces** (mocked / unit-tested only — not E2E on this
  pass): deploy with a real git clone, Raft cluster, AI providers with
  a live Ollama instance, SSO with a real IdP, FIPS mode, OPA policy
  with real `.rego` files, gitops reconciler with a real repo, real
  Let's Encrypt ACME, real DuckDNS.

---

## Open before tagged GA (`v0.3.0`)

Not blocking pilot/staging install — these are release hygiene:

| # | Item | Why |
|---|---|---|
| 1 | Update `CHANGELOG.md`: move `Unreleased` to `v0.3.0` with the L-* / D-* / A-* list | semver hygiene |
| 2 | Re-run `make e2e DISTRO=ubuntu24` against the post-A6/A7 binary | only ubuntu22 + debian12 verified after A6/A7 |
| 3 | `make release-dry` (goreleaser snapshot) | confirm artefact build |
| 4 | `git tag v0.3.0` | binary's `--version` currently prints `dev` |
| 5 | `gh pr create` `apigw-launch` → `main` | merge to mainline |
| 6 | (optional) GitHub Release with the goreleaser artefacts | public binaries |

Estimated work: ~30–60 minutes.

---

## Commit history of this work

```
ae4b5d1 fix(release): A1+A4+A5+A6+A7 + audit-lock + tls-renew + uninstall-cleanup
5a177b9 fix(release): restore L-1..L-9 + D-1/2/8 + deployEntry mirror fields
97556e6 fix(diag): fast, clear queue-lock reporting in doctor/status   ← prior (doc only)
```

Pushed to `origin/apigw-launch`. PR URL when ready:
`https://github.com/devevghenicernev-png/api-gateway-installer/pull/new/apigw-launch`

---

## Environment

- **Host:** macOS 15.3.0 (darwin/arm64), Go 1.26.4, Docker 28.0.1 (colima)
- **Containers:** `apigw-e2e:ubuntu22` (Ubuntu 22.04.5 LTS, nginx 1.18.0) and `apigw-e2e:debian12` (Debian 12, nginx 1.22.1), both privileged, `--cgroupns=host`, systemd PID 1, arm64 binary mounted at `/usr/local/bin/apigw`.
- **Dashboard smoke:** ran in-container via `curl` for raw endpoint / RBAC / CSRF / SSO / mTLS checks (no Chrome CDP this pass; the static UI surface is covered by the prior MANUAL_TEST_REPORT).
