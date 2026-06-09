# Security Policy

## Reporting a vulnerability

If you believe you have found a security vulnerability in `apigw`, please
report it privately. Do **not** open a public GitHub issue.

**Preferred channel:** GitHub Security Advisories — open a draft advisory
on the repository's *Security* tab. This keeps the conversation private
between you and the maintainers until a fix is released.

**Email fallback:** `security@<TODO-set-your-domain>` (PGP key TBD).
Please include `[apigw security]` in the subject line.

We aim to:

- Acknowledge your report within **3 business days**.
- Provide an initial assessment (impact + likely fix window) within
  **10 business days**.
- Release a fix for confirmed High/Critical issues within **30 days**
  whenever feasible, coordinated with the reporter.

## Scope

In scope:

- The `apigw` binary and everything under `cmd/` and `internal/`.
- The generated nginx config, systemd / openrc / launchd units, and the
  installer / upgrade flow.
- The admin dashboard (`/dashboard`) and the admin API (`/api/admin/*`
  and `/api/v1/admin/*`).
- The webhook delivery surface (HMAC verification, queue, dispatcher).
- The release artifacts on GitHub (binaries, cosign signatures, checksums).

Out of scope:

- Vulnerabilities in third-party dependencies (nginx itself, the host OS,
  systemd, Let's Encrypt, the IdP). Please report those upstream.
- Findings that require physical access to the host or a pre-existing
  root shell.
- Self-DoS through extreme configuration (e.g. setting rate limits to
  `0`). Those are documented foot-guns; if you find one that is *not*
  documented as such, that is in scope.
- Social-engineering or phishing the maintainers.

## Supported versions

`apigw` is pre-1.0. We patch security issues only against the **latest
tagged release** and the `main` branch. Once a `v1.0` is published this
policy will move to a documented N / N-1 support window.

| Version  | Supported |
|----------|-----------|
| `main`   | ✅ |
| `v0.3.x` | ✅ |
| `< v0.3` | ❌ |

## Safe harbor

We will not pursue legal action against researchers who:

- Make a good-faith effort to avoid privacy violations, data loss, and
  service disruption.
- Do not exfiltrate more data than is necessary to demonstrate the
  vulnerability.
- Do not exploit the vulnerability beyond what is required for proof.
- Give us a reasonable window to issue a fix before public disclosure
  (90 days is the default; we will coordinate if a shorter or longer
  window makes sense).

If in doubt, ask first.

## Cryptography & supply chain

- All release binaries are published with sha256 checksums and a
  keyless **cosign** signature (GitHub OIDC, no offline keys to lose).
  The installer (`scripts/install.sh`) verifies both before installing.
- The build is provenance-attested via SLSA Level 3 (see
  `.github/workflows/slsa.yml`).
- Go dependencies are scanned on every CI run with `govulncheck`.

## Hall of fame

Researchers who responsibly disclose valid issues will be credited in
the release notes for the fix, unless they request to stay anonymous.
