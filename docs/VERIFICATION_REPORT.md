# Verification Pass — apigw v0.3 candidate

**Date:** 2026-06-08
**HEAD:** `3cfd72d` (+ uncommitted fixes in working tree)
**Method:** full re-test on current working tree — build (host + linux/arm64),
`go vet`, `gofmt`, full unit suite, Docker systemd e2e, hand-driven operator
walkthrough in a privileged systemd container (ubuntu22, nginx 1.18), and a
live dashboard smoke (RBAC/CSRF/SSO) on macOS.

Goal: confirm every finding from `BUGS_FOUND.md` / `DOCKER_TEST_REPORT.md` /
`MANUAL_TEST_REPORT.md` is actually fixed on the current tree, and surface
anything still broken.

## Verdict

The product installs and runs **from a clean box**. All install/dataplane
blockers are genuinely fixed and re-verified. One real bug was still live in
this pass (**BUG-4**, `APIGW_CONFIG_DIR` ignored) — fixed in code during this
pass (`internal/config/config.go::SearchPaths`). That fix also closes the
**BUG-5** reloader warning (it was a symptom of BUG-4).

## Automated gates

| Gate | Result |
|------|--------|
| `make build` (darwin) + `linux/arm64` static | ✅ both build |
| `go vet ./...` | ✅ clean |
| `gofmt -l internal cmd test` | ✅ clean (empty) |
| `go test -race ./...` | ✅ 28 pkgs ok, no failures |
| `make e2e DISTRO=ubuntu22` (systemd-in-Docker) | ✅ TestScenarios + TestMigrate + TestWebhook green |

> e2e on macOS needs `APIGW_E2E_BINARY=$PWD/bin/apigw-linux-arm64` — the default
> harness path (`bin/apigw`) is the darwin build and dies with `exec format
> error` inside the linux container. Not a product bug; build/harness ergonomics.

## Manual operator walkthrough (ubuntu22, nginx 1.18, root, systemd)

Every step driven by hand via `docker exec`, exactly like an operator.

| Flow | Result | Prior finding re-verified |
|------|--------|---------------------------|
| `install` on a box with nginx **stopped** | ✅ `apigw is ready`, nginx started | L-3 fixed |
| `nginx -t` after install | ✅ ok | BUG-1 (validate plain `nginx -t`), BUG-1b (gzip in server scope) fixed |
| stock `default` site | ✅ disabled, only `apigw.conf` enabled | BUG-6 fixed |
| `curl` on raw IP, no `Host` | ✅ 200 (apigw is `default_server`) | BUG-6 fixed |
| dashboard systemd unit | ✅ active, `/var/log/apigw` created, no `226/NAMESPACE` | L-1 fixed |
| `install` auto-starts dashboard | ✅ `dashboard service started on :9080` | L-4 fixed |
| `api add` + proxy (Host + raw IP) | ✅ 200 `hello-upstream` both | — |
| `api disable` / `enable` / dataplane | ✅ 404 after disable, 200 after enable | L-6 handled (poll) |
| `tls enable self-signed` | ✅ `listen 443 ssl http2;`, `nginx -t` ok on 1.18, HTTPS 200, HTTP→HTTPS **301** | BUG-7 fixed |
| `tuning apply --reload` | ✅ stock `worker_processes` neutralized (`# apigw-tuning-disabled:`), single active directive | L-2 fixed |
| **`systemctl restart nginx` after tuning** | ✅ survives — no latent outage | L-2 fixed (the dangerous part) |
| `tuning revert` | ✅ restores original `nginx.conf`, `nginx -t` ok | L-2 recovery |
| `status` rendering | ✅ webhook `• served by dashboard`, disabled timer neutral `•` (not red ×) | L-8 fixed |
| `consumer add acme` | ✅ `NAME=acme` (defaults to id) | L-9 fixed |
| `doctor` (dashboard holds bbolt locks) | ✅ 11 ok / 0 fail, degrades gracefully | see L-7 below |
| `backup` / `restore --force` | ✅ packs config+certs, restores 5 files | — |
| `uninstall` | ✅ pre-uninstall backup, units removed, nginx site removed, **stock `default` restored**, ports freed, state preserved | — |

## Live dashboard smoke (macOS, security on: owner+viewer tokens, OIDC SSO, sessions)

| Check | Result | Prior finding |
|-------|--------|---------------|
| `/api/status` → `sso_enabled: true` + SSO button in `index.html` | ✅ | D-2(a) fixed |
| `Identify()` consults session cookie (`SessionResolver` wired in `serve.go`) | ✅ in code | D-2(b) fixed |
| JWT verifier memoized per API by config-hash (`server.go::jwtVerifier`) | ✅ in code — JWKS cache survives requests | D-1 fixed |
| anonymous write | ✅ 403 | — |
| owner write **without** `X-CSRF-Token` | ✅ 403 (CSRF enforced) | — |
| viewer read / viewer write | ✅ 200 / 403 (RBAC enforced) | — |
| SSO login → unreachable IdP | ✅ 502 clean discovery error | — |
| SSO callback → bad state | ✅ 400 | — |
| startup banner at default WARN level | ✅ `apigw dashboard listening on …` prints | D-8 fixed |

## New finding fixed in this pass

### BUG-4 🟠 (was 🟢) — `SearchPaths()` ignored `APIGW_CONFIG_DIR` → silent empty config

**Component:** `internal/config/config.go::SearchPaths`

`SearchPaths()` only checked `XDG_CONFIG_HOME` + `/etc/apigw/config.yaml`. With
`APIGW_CONFIG_DIR` set (custom layout / systemd unit / rootless / CI), `Load()`
found **no** file and silently fell back to `Defaults()` — i.e. **security
block empty: no admin tokens, `rbac_enforce:false`, `sso:nil`**. The admin API
then ran wide-open (legacy mode) while the operator believed their `security:`
config was active. This also drove the **BUG-5** symptom: the security reloader
watches `cfg.Path()`, which defaulted to `/etc/apigw` → `watch dir failed dir=/etc/apigw`.

**Repro (before fix):**
```
APIGW_CONFIG_DIR=/tmp/cfg apigw config export   # security: all empty, sso: null
```

**Fix (applied):** prepend `paths.ConfigDir()/config.yaml` to `SearchPaths()`,
honoring `APIGW_CONFIG_DIR` consistently with every other `APIGW_*` override
(and matching the `paths.go` header promise). On a standard install
(`/etc/apigw`, no override) behavior is unchanged.

**Re-verified after fix:** `APIGW_CONFIG_DIR=… apigw config export` shows the
full `security.sso` block + `rbac_enforce:true`; the dashboard reports
`sso_enabled:true`, enforces RBAC (viewer write → 403), and the reloader warning
is gone. Full unit suite + e2e still green.

### L-7 🟢 — `doctor`/`status` stall + cryptic on bbolt lock contention — fixed in this pass

**Component:** `internal/webhook/queue.go`, `internal/diag/checks.go`, `internal/cmd/status/status.go`

With the dashboard running (it holds `jobs.db`/`audit.db`/`approvals.db`
flocks), `apigw doctor` blocked ~5 s then printed `queue-depth: queue not
opened`, and `apigw status` silently stalled ~5 s — both used the default 5 s
bbolt open timeout, and the doctor check swallowed the specific error.

**Fix (applied):** added `webhook.ErrQueueLocked` (sentinel, wrapped on
`bbolt.ErrTimeout`) plus `OpenQueueTimeout` / `OpenQueueAtTimeout`. Doctor and
status now open the queue with a 300 ms timeout and `errors.Is(err,
ErrQueueLocked)`: doctor reports `✓ queue-depth  busy — held by running
dashboard`, status returns fast.

**Re-verified:** in-container with the dashboard holding the lock —
`doctor` **288 ms**, `status` **292 ms** (was ~5 s each), doctor line reads
`busy — held by running dashboard`. Unit suite + e2e green.

## Still open

None from the prior reports. All `BUGS_FOUND.md` / `DOCKER_TEST_REPORT.md` /
`MANUAL_TEST_REPORT.md` findings are fixed and re-verified on this tree.

## Environment

- Host: macOS (darwin/arm64), Go 1.26.4, Docker 28.0.1 (colima).
- Container: `apigw-e2e:ubuntu22` (Ubuntu 22.04, nginx 1.18.0), privileged,
  `--cgroupns=host`, systemd PID 1, arm64 static binary mounted at
  `/usr/local/bin/apigw`.
- Dashboard smoke: `apigw dashboard serve` on `127.0.0.1:9080`, security config
  via `XDG_CONFIG_HOME` and (post-fix) `APIGW_CONFIG_DIR`, `curl` for raw
  endpoint/RBAC/CSRF/SSO checks.
