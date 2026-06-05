# Installing apigw

This guide walks an operator from a clean Debian/Ubuntu box to a running
gateway with one API, optional TLS, and the live dashboard. ~10 minutes
end-to-end.

## 1. Prerequisites

- A Debian 12 / Ubuntu 22.04 / Ubuntu 24.04 host with a public IP
  (Let's Encrypt requires HTTP-01 reachability) or a DuckDNS subdomain.
- Root access (apigw writes systemd units + nginx config).
- nginx (`sudo apt-get install -y nginx`) — apigw does NOT install it for you.
- Cosign (`brew install cosign` / [docs](https://docs.sigstore.dev/cosign/installation/))
  to verify the release tarball during install. Skippable via
  `APIGW_SKIP_COSIGN=1` but not recommended.

## 2. Drop the binary

```sh
curl -fsSL \
  https://raw.githubusercontent.com/devevghenicernev-png/apigw/main/scripts/install.sh \
  | sudo sh
```

The shim downloads `apigw_<version>_<os>_<arch>.tar.gz`, fetches
`checksums.txt{,.sig,.pem}` from the same release, runs `cosign
verify-blob` with the GitHub Actions OIDC identity bound to our
`release.yml` workflow, then verifies the tarball's SHA-256 against the
signed manifest. Only then does it `install -m 0755` to
`/usr/local/bin/apigw`.

Verify:

```sh
apigw version
```

## 3. Run the wizard

```sh
sudo apigw install
```

Five steps (`Esc` rewinds, `Ctrl-C` cancels cleanly):

1. **HTTP port + server_name** — defaults to `80` and `_` (catch-all).
2. **TLS strategy** — Let's Encrypt / DuckDNS / self-signed / skip.
   The wizard validates DNS for LE and DuckDNS API tokens before
   continuing.
3. **Dashboard** — enable + port (default 9080). Powered by SSE; no
   polling, no refresh.
4. **Webhook** — enable + port (default 9000). HMAC-verified GitHub push
   receiver.
5. **Storage** — config path (default `/etc/apigw/config.yaml`).

After the plan card, confirm with `Y`. The wizard writes
`/etc/apigw/config.yaml`, renders the nginx site, validates with `nginx
-t`, reloads, and (if you picked a TLS strategy) obtains the certificate
via webroot HTTP-01 — nginx stays up the whole time.

For unattended install (CI, image baking):

```sh
sudo apigw install --config-file install.yml --yes
```

Save defaults via `apigw install --print-config > install.yml`.

## 4. Add the first upstream

```sh
sudo apigw api add hello --port 3000
```

Curl works immediately:

```sh
curl http://api.example.com/api/hello
```

## 5. Run the dashboard

```sh
sudo apigw dashboard start
apigw dashboard open
```

`dashboard start` installs `/etc/systemd/system/apigw-dashboard.service`
(consolidated daemon: dashboard HTTP + webhook receiver + deploy worker +
TLS expiry ticker), enables it, and reloads nginx so `/dashboard` proxies
to the local port. `dashboard open` runs `xdg-open` / `open` against the
public URL.

The page boots from `/api/status` (one-shot snapshot) then subscribes to
`/events?topic=…` for live updates. Survives Wi-Fi blips via
`Last-Event-ID` replay from the in-process ring buffer (1024 events per
topic).

## 6. Health checks

```sh
apigw status              # one-shot table (services, TLS, deploys, webhook queue)
apigw status --watch      # in-place re-render every 2s (lipgloss cursor positioning)
apigw doctor              # actionable checklist with fix commands
```

`apigw doctor` runs 14 checks in parallel (nginx config integrity, port
listen, TLS expiry, SSH deploy key, config-perms, queue depth, disk free,
…) and exits **0** if everything's OK, **1** if any warnings, **2** if any
failures.

## 7. Next steps

- [Deploy a git repo](./deploy.md) — `apigw deploy add` lifecycle.
- [GitHub webhooks](./webhooks.md) — `apigw webhook setup <deploy>` walks
  through paste-into-GitHub.
- [Migrating from the bash installer](./migration-from-bash.md) — if this
  host already had `/etc/api-gateway/`.

## Uninstall

```sh
sudo apigw uninstall            # keeps /etc/apigw and /var/lib/apigw
sudo apigw uninstall --purge    # also wipes state (irreversible)
```

Both paths auto-create a pre-uninstall backup tarball you can keep for
re-install.
