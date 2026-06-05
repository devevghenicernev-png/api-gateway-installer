# Migrating from the bash installer

If this host previously ran the bash `install.sh` / `api-manage` flow,
`apigw migrate` translates it in place without losing data.

## What gets migrated

- Every entry in `/etc/api-gateway/apis.json` becomes a `config.API`
  (regular upstream) or, if `type=ai-model` and the name matches a known
  provider, an AI-managed entry (`apigw ai list` finds it).
- Every `/etc/api-gateway/deployments/<name>.json` becomes a
  `config.Deploy` with runtime hint preserved.
- Each deployment's `webhook_secret` field becomes
  `/etc/apigw/webhooks/<name>.secret` (0600). Existing secrets on the
  new side are NOT overwritten — the bash payload is a fallback only.
- The `api-manage` command stays usable as a thin compatibility symlink
  at `/usr/local/bin/api-manage` — it translates the legacy verbs into
  cobra invocations and prints a one-line deprecation banner to stderr.

## What's NOT migrated

- Per-deployment release directories under `/opt/deployments/<name>` —
  these rebuild from git on the next deploy.
- `PM2`-managed processes — apigw uses systemd template units
  (`apigw-deploy@<name>.service`) instead. Old PM2 entries keep running
  until you `apigw deploy run <name>` (then the new unit takes over).
- The legacy `/etc/nginx/sites-available/apis` file — apigw renders its
  own `/etc/nginx/sites-available/apigw.conf` from the translated config.
  The old file is left alone; remove it manually after verifying the new
  site works.

## Procedure

```sh
sudo apigw migrate
```

Sequence:

1. **Detect** — probes `/etc/api-gateway/apis.json`,
   `/etc/api-gateway/deployments/`, `/usr/local/bin/api-manage`. Empty
   detect = "no legacy installation, nothing to do" exit.
2. **Plan card** — shows count of APIs and deploys to import, plus a list
   of names that are already present on the new side (skipped).
3. **Pre-migration backup** — auto-runs `apigw backup` and writes
   `apigw-pre-migrate-<ts>.tar.gz` in cwd. Skip with `--skip-backup` if
   you already have one.
4. **Confirm** — default-YES prompt; bypass with `--yes`.
5. **Apply** — mutates config + writes secrets + reloads nginx.
6. **Rename legacy `apis.json`** — moved to
   `/etc/api-gateway/apis.json.migrated-<ts>`. Never deleted; inspect or
   roll back manually if needed.
7. **Install shim** — creates `/usr/local/bin/api-manage` symlink. Skip
   with `--skip-shim`.

Idempotent: a second run reports "All legacy entries already match" and
exits 0.

## Verifying

```sh
apigw api list
apigw deploy list
apigw doctor
```

The legacy CLI keeps working through the shim:

```sh
api-manage list           # → apigw api list (with deprecation banner on stderr)
api-manage deploy run x   # → apigw deploy run x
```

Once you're confident, retire the bash artefacts:

```sh
sudo rm -rf /etc/api-gateway     # legacy config dir
sudo rm -f /etc/nginx/sites-available/apis /etc/nginx/sites-enabled/apis
sudo nginx -t && sudo systemctl reload nginx
```

The shim symlink at `/usr/local/bin/api-manage` can stay indefinitely —
it just routes to the new binary. Remove it with `sudo rm
/usr/local/bin/api-manage` when no scripts depend on the old name.

## If something goes wrong

The legacy `apis.json` (renamed to `apis.json.migrated-<ts>`) and the
auto-backup tarball (`apigw-pre-migrate-<ts>.tar.gz`) together let you
roll back:

```sh
# 1. restore the new-side state from before migration
sudo apigw restore apigw-pre-migrate-20260605-103000.tar.gz --force

# 2. put apis.json back at its original name
sudo mv /etc/api-gateway/apis.json.migrated-* /etc/api-gateway/apis.json

# 3. uninstall the new apigw, leaving the bash install intact
sudo apigw uninstall --purge
```

After this the old bash flow is back to what it was before `apigw migrate`
ran.
