# `apigw` — Architecture & Design Specification

> Top-to-bottom architecture for `apigw`, the Go-based replacement for the
> bash API-Gateway installer. This is the engineering spec — every choice has
> a stated rationale and reference. Where it disagrees with `DESIGN.md`, this
> document wins.

## Where this document came from

This spec is the synthesised output of a six-agent research consilium, with
each agent investigating one domain against the current state of the art
(top OSS CLIs in 2026 — `gh`, `flyctl`, `caddy`, `terraform`, `kubectl`,
Charm ecosystem; and authoritative protocol/library docs — Let's Encrypt,
ACME, systemd, WHATWG SSE, GitHub webhooks, SLSA, Sigstore). Every
recommendation is grounded in a real implementation, doc, or RFC — citations
inline.

## How to read this doc

Seven self-contained briefs. They reference each other but each can be read
in isolation. Read order for a new contributor:

1. **Guiding principles** (below) — the non-negotiables.
2. **Go architecture & code organization** — where code lives, how it's wired.
3. **CLI design system** — interaction contract, error voice, color palette.
4. **TLS automation** — the cert subsystem.
5. **Deploy subsystem** — runtime detection, supervision, zero-downtime.
6. **Live streaming** — the pub/sub hub, SSE, dashboard.
7. **Security & distribution** — supply chain, signing, secrets, hardening.

## Guiding principles (the non-negotiables)

1. **Feature parity first.** Everything the bash installer can do, `apigw`
   does. No regressions. The mapping table in `DESIGN.md §8` is the contract.
2. **HTTPS is first-class, never a patch.** TLS lifecycle (`enable`, `renew`,
   `add-domain`, `status`, `disable`) is built into the CLI from day one.
   Either at install time or any moment later — one command.
3. **Live everywhere — no manual refresh, no polling.** Every observable
   surface (logs, status, build output, deploy state) streams in real time
   over a single in-process event hub fanned out to CLI and SSE.
4. **Plan, then apply.** Every mutation shows a plan card and asks for
   confirmation. `--yes` bypasses; `--dry-run` exits after the plan.
5. **One binary, single source of truth.** No Node.js dependency on the
   server; dashboard + webhook server + CLI are the same Go binary.
6. **TTY-adaptive output.** Identical command works for humans (boxed,
   colored, spinners) and scripts (`--json`, plain, no prompts).
7. **Reversible by default.** Every install/upgrade writes an atomic
   snapshot first; rollback is a single command.
8. **Trust nothing on the wire.** All releases cosign-signed with SLSA L3
   provenance; install scripts verify checksums before exec; webhook HMAC
   verified in constant time.

---

# Go architecture & code organization

Synthesised from `cli/cli` (gh), `superfly/flyctl`, `hashicorp/terraform`,
`gohugoio/hugo`, `tailscale/tailscale`, `charmbracelet/gum`. The `gh`+`flyctl`
pattern is the closest fit for our scope; everything below is grounded in
files you can open in those repos.

## Repo layout

```
apigw/
├── cmd/apigw/main.go              # thin entrypoint, calls internal/apigwcmd.Main()
├── internal/
│   ├── apigwcmd/cmd.go            # Main() + exit-code mapping (mirror cli/cli/internal/ghcmd/cmd.go)
│   ├── cmdutil/
│   │   ├── factory.go             # Factory DI container (see below)
│   │   ├── errors.go              # FlagError, SilentError, CancelError
│   │   └── flags.go               # StringEnumFlag etc.
│   ├── cmd/                       # subcommand tree — one directory per noun
│   │   ├── root/root.go           # NewCmdRoot(f *Factory); AddGroup()s
│   │   ├── install/install.go     # apigw install (huh wizard)
│   │   ├── api/
│   │   │   ├── api.go             # parent NewCmdAPI(f)
│   │   │   ├── add/add.go
│   │   │   ├── list/list.go
│   │   │   └── remove/remove.go
│   │   ├── tls/
│   │   ├── deploy/
│   │   ├── dashboard/
│   │   ├── webhook/
│   │   ├── status/
│   │   └── doctor/
│   ├── config/                    # koanf-based config loader
│   ├── nginx/                     # config generation, atomic reload
│   ├── system/                    # systemd, file perms, pkg manager
│   ├── tls/                       # ACME (lego), renewal, storage
│   ├── deploy/                    # runtime detect, build, supervise
│   ├── events/                    # the pub/sub hub (~100 LoC)
│   ├── tui/                       # huh wizards, reusable forms
│   ├── iostreams/                 # mirrors gh's iostreams (IO + color + TTY)
│   ├── build/version.go           # ldflags target: Version, Commit, Date
│   └── assets/                    # go:embed nginx/systemd/dashboard templates
├── test/
│   ├── e2e/                       # docker-based; Dockerfile.{debian,ubuntu}
│   └── fixtures/
├── .goreleaser.yaml
├── go.mod
└── Makefile
```

**Why no `pkg/`?** `flyctl` is 100k+ LoC and ships zero `pkg/`. Exporting a
package is a public-API promise; we don't make one yet. Add `pkg/` the day
someone needs to import `apigw` as a library — not before.

## The Factory pattern (DI without a framework)

Lifted directly from `cli/cli/pkg/cmdutil/factory.go`:

```go
// internal/cmdutil/factory.go
type Factory struct {
    AppVersion string
    BuildDate  string
    IOStreams  *iostreams.IOStreams         // eager
    Prompter   prompter.Prompter            // eager, wraps huh
    Logger     *slog.Logger                 // eager

    // Lazy deps — func returns let `apigw version` work even if config breaks
    Config   func() (*config.Config, error)
    Nginx    func() (nginx.Manager, error)
    Systemd  func() (system.Systemd, error)
    Events   func() (events.Hub, error)
    FS       afero.Fs                       // injectable for tests
}
```

Two non-obvious wins:
- **Lazy `func() (T, error)` for heavy deps** means `apigw version` works
  even if config is corrupt. Same trick `gh` uses for HTTP client.
- **`afero.Fs`** as the filesystem abstraction lets tests use `afero.NewMemMapFs()`
  — critical for unit-testing nginx-config-generation without touching `/etc/`.

Every subcommand follows:

```go
func NewCmdAPIAdd(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
    opts := &Options{IO: f.IOStreams, Config: f.Config}
    cmd := &cobra.Command{
        Use:   "add <name>",
        Short: "Register a new upstream API",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            opts.Name = args[0]
            if runF != nil { return runF(opts) }       // test hook
            return runAdd(opts)                        // real impl
        },
    }
    cmd.Flags().IntVar(&opts.Port, "port", 0, "upstream port")
    return cmd
}

func runAdd(opts *Options) error { /* business logic, fully testable */ }
```

The `runF` indirection lets tests substitute `runAdd` without standing up
Cobra. This is the pattern used everywhere in `cli/cli/pkg/cmd/pr/*/`.

## Error handling — three behavioural types

```go
// internal/cmdutil/errors.go
type FlagError struct{ err error }            // → exit 2, print usage
func (e *FlagError) Error() string  { return e.err.Error() }
func (e *FlagError) Unwrap() error  { return e.err }
func FlagErrorf(f string, a ...any) error { return &FlagError{fmt.Errorf(f, a...)} }

var SilentError = errors.New("SilentError")   // → exit 1, no print (already printed)
var CancelError = errors.New("CancelError")   // → exit 2, user hit Esc/Ctrl-C
```

Top-level `Main()` maps:

```go
const (
    exitOK     = 0
    exitError  = 1
    exitUsage  = 2
    exitConfig = 3
    exitNet    = 4
    exitAuth   = 5
    exitSIGINT = 130
)
```

Wrap with `fmt.Errorf("nginx reload: %w", err)` — never bare `errors.New`.
Cobra's `SuggestionsMinimumDistance = 1` gives "did you mean?" for free on
the command tree; we extend it to resource names (deployment IDs, API names)
via `agnivade/levenshtein`.

## Logging — slog + charmbracelet/log

```go
func NewLogger(io *iostreams.IOStreams, jsonMode bool, level slog.Level) *slog.Logger {
    if jsonMode || !io.IsStderrTTY() {
        return slog.New(slog.NewJSONHandler(io.ErrOut, &slog.HandlerOptions{Level: level}))
    }
    h := log.NewWithOptions(io.ErrOut, log.Options{
        ReportTimestamp: false,
        Level:           log.Level(level),
    })
    return slog.New(h)
}
```

JSON on stderr by default when not a TTY → systemd-journald gets structured
logs for free, `journalctl -u apigw -o json` is the audit log. Don't use zap
or zerolog: the ecosystem is converging on `slog.Handler`; everything new
should be a slog handler.

## Config — koanf, not viper

`knadh/koanf/v2`. Verifiable reasons:

| Lever | viper | koanf |
| --- | --- | --- |
| Binary size impact | +313% (pulls every parser) | parsers are opt-in modules |
| Case handling | lowercases all keys (corrupts YAML/TOML spec) | preserves case |
| Precedence model | implicit, surprising | last `Load()` wins — explicit |
| API surface | huge, mutable | small, immutable-feeling |

Load order = precedence (defaults → file → env → flags = flags win):

```go
k := koanf.New(".")
k.Load(structs.Provider(defaults, "koanf"), nil)
k.Load(file.Provider("/etc/apigw/config.yaml"), yaml.Parser())
k.Load(env.Provider("APIGW_", ".", envMap), nil)
k.Load(posflag.Provider(cmd.Flags(), ".", k), nil)
```

Config file: XDG first (`~/.config/apigw/config.yaml`), fall back to
`/etc/apigw/config.yaml` (system-wide). `apigw install` writes the latter.

## Testing — three layers

| Layer | Tool | What it tests |
| --- | --- | --- |
| **Unit** | stdlib `testing` + `testify/require` | runOptions(), nginx-config generation, parser edge cases |
| **Command** | `cmd.SetArgs()` + mock Factory | flag parsing, error surfaces, output snapshots |
| **Golden** | `hexops/autogold/v2` | nginx confs, systemd units, JSON output — `-update` regenerates |
| **E2E** | Docker (Ubuntu 22.04, 24.04, Debian 12) | `apigw install` on a clean box, curl /, assert 200 |

Mock HTTP via `gh`'s `pkg/httpmock` pattern, not `jarcoal/httpmock` — the
gh version is simpler and integrates with the Factory.

Fuzz the nginx-config generator (`go test -fuzz=FuzzGenerate`) — random
inputs into the config writer, assert `nginx -t` always passes on output.
This catches injection bugs in template strings.

## Anti-patterns we will reject

1. **Global `rootCmd` with `init()` registration.** Build inside `NewCmd*`
   factories. Tests need fresh trees per case.
2. **Mixing flag parsing with business logic in `RunE`.** Always extract
   `runOptions()`. Cobra is the UI; the function under it is the unit.
3. **`os.Exit()` outside `Main()`.** Return errors up. `defer`s leak otherwise.
4. **Viper.** Settled — see table above.
5. **`pkg/foo` for code only we use.** Use `internal/`.
6. **Hand-rolled help formatting.** Cobra's templates work. Override the
   template once, globally, in `root.go`.
7. **Logging via `fmt.Fprintln(os.Stderr, ...)` for diagnostics.** Use the
   injected `*slog.Logger`. Direct writes are for user-facing UI only.

## Reference files to keep open while implementing

- `cli/cli/pkg/cmdutil/factory.go`
- `cli/cli/pkg/cmdutil/errors.go`
- `cli/cli/internal/ghcmd/cmd.go`
- `cli/cli/pkg/cmd/root/root.go`
- `cli/cli/pkg/cmd/pr/list/list_test.go`
- `cli/cli/pkg/httpmock/`
- `superfly/flyctl/internal/command/`
- `knadh/koanf` v2 README

---

# CLI design system (UX)

The product feel target is `gh` + `flyctl` + Stripe + Supabase + Charm. The
user explicitly asked for "mega fast, smooth, premium." Premium = quiet,
opinionated, never re-asks, instantly understandable error states.

## 15 design rules (each non-negotiable)

1. **Verb-second, noun-first, singular.** `apigw deploy add`, never
   `apigw add-deploy` or `apigw deploys list`. Verb-second won (`docker
   container ls` beat `docker ps`).
2. **TTY-adaptive output is the contract.** TTY → colored, boxed, spinner.
   Piped or `NO_COLOR=1`/`TERM=dumb` → plain, tab-separated, no spinners,
   no boxes. `APIGW_FORCE_TTY=1` overrides (mirrors `GH_FORCE_TTY`).
3. **Plan-then-apply for every mutation.** Print a plan card; "Apply? [Y/n]".
   `--yes` bypasses. `--dry-run` exits after the plan. Eliminates dozens of
   ad-hoc "are you sure?" prompts.
4. **Type-the-name destructive confirms.** `apigw deploy remove api.example.com`
   → user must retype `api.example.com`. Bypass with `--yes` for everything
   except `uninstall` (no bypass).
5. **Wizards = `huh` Groups, not chained prompts.** Esc goes back, Ctrl-C
   cancels cleanly, validation runs on submit (not per keystroke — flickers).
   Always end with a plan card.
6. **Errors have four parts: what, why, fix, link.** Lowercase, no
   exclamation, no stack (unless `--debug`). Always suggest a next command.
7. **"Did you mean?" everywhere.** Levenshtein ≤ 2 on unknown subcommands
   and unknown resource names. Cobra handles commands; we extend to names.
8. **One spinner, never nested.** `bubbles/spinner` Dot style for indeterminate
   (<10 s). `bubbles/progress` for known-step work ("3 of 7"). Raw streamed
   output (prefixed `│ `) for long subprocesses. Never overlap.
9. **`-q` contract: only print IDs to stdout, errors to stderr, exit code is
   truth.** `apigw deploy add -q` prints just the deployment ID. Scriptable.
10. **Verbose ladder: `-v` info, `-vv` debug, `-vvv` trace.** `--debug` alias
    for `-vv`. Stop at `-vvv` — no `-vvvv`.
11. **`--json` mode is structured, filterable, stable.** `--json id,host,status`
    selects fields (gh-style). `--jq '.status'` for inline filters. JSON
    schema is versioned; never break between minors.
12. **`--watch` uses lipgloss cursor positioning, never clear-screen.** Naive
    clear flickers. Repaint only changed rows. Biggest "premium feel" lever.
13. **Help leads with examples.** Override Cobra's default template: `Examples:`
    appears under the one-line description, before flags. Copy `gh`'s exact
    layout.
14. **First-run experience: a 6-line welcome with one suggested command, then
    one opt-in telemetry ask** (n default).
15. **Single binary; completions for bash/zsh/fish/PowerShell via
    `apigw completion <shell>`.** Cobra builtin. Ship manpages via
    `cobra/doc`.

## Side-by-side comparisons

### `apigw deploy add` — happy path

```
BAD                                        PREMIUM
─────────────────────────────────────────  ──────────────────────────────────────────
> apigw deploy add                         > apigw deploy add
Host? api.example.com                      ┌─ New deployment ──────────────────────┐
Upstream? localhost:3000                   │ Host        api.example.com           │
TLS? y                                     │ Upstream    http://localhost:3000     │
Adding deployment...                       │ TLS         Let's Encrypt (auto)      │
Done.                                      │ Rate limit  100 r/s                   │
                                           └───────────────────────────────────────┘
                                             Apply this plan? [Y/n] _

                                             ✓ wrote /etc/nginx/sites/api.example.com.conf
                                             ✓ reloaded nginx (pid 4821)
                                             ✓ issued certificate (expires 2026-09-02)

                                             Deployment ready  →  https://api.example.com
                                             Next: apigw deploy logs api.example.com
```

### Error: port in use

```
BAD                                        PREMIUM
─────────────────────────────────────────  ──────────────────────────────────────────
Error: bind: address already in use        × cannot bind port 8080
panic: net.OpError ...                       port is held by nginx (pid 4821)
goroutine 1 [running]:
  ...                                        try one of:
                                                apigw doctor          inspect ports
                                                apigw deploy add --port 8081

                                             docs: https://apigw.dev/errors/E_PORT_BUSY
```

### `apigw tls enable` — streaming subprocess

```
BAD                                        PREMIUM
─────────────────────────────────────────  ──────────────────────────────────────────
running certbot...                         Enabling TLS for api.example.com
Saving debug log to /var/log/letsenc...      ⠋ requesting certificate
Plugins selected: Authenticator nginx...   │ acme: order created
Requesting a certificate for api.exa...    │ acme: challenge passed (http-01)
Performing the following challenges:       │ acme: certificate issued
http-01 challenge for api.example.com        ✓ certificate installed
Waiting for verification...                  ✓ nginx reloaded
Cleaning up challenges
Subscribe to the EFF mailing list...         TLS active  →  https://api.example.com
IMPORTANT NOTES: ...                         Auto-renew: enabled (next: 2026-08-04)
```

## Library shortlist (Charm + supporting)

| Library | Use |
| --- | --- |
| `charmbracelet/huh` | every interactive form; theme `huh.ThemeCharm()` lightly customized |
| `charmbracelet/lipgloss` | all styling; `AdaptiveColor{Light, Dark}` everywhere |
| `charmbracelet/bubbles/spinner` | Dot style; one global registry to prevent nesting |
| `charmbracelet/bubbles/progress` | multi-step bars with brand-gradient fill |
| `charmbracelet/bubbletea` | only for `--watch`, log follow, wizard runner |
| `muesli/termenv` | color profile detection (truecolor/256/16/none) |
| `mattn/go-isatty` | independent TTY detection for stdin/stdout/stderr |
| `spf13/cobra` + `cobra/doc` | command tree, completions, manpages |

Don't bubbletea-ify simple commands — its startup cost is noticeable on
ARM boards. Reserve for long-running interactive views.

## Color palette (lipgloss, all `AdaptiveColor`)

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `Primary` | `#0B7285` | `#22D3EE` | brand, headings, selected wizard option (teal — distinct from gh blue, fly purple, stripe indigo) |
| `Accent` | `#7C3AED` | `#A78BFA` | links, "Next:" hints |
| `Success` | `#0E7C3A` | `#34D399` | `✓` marks, "ready" badges |
| `Warn` | `#B45309` | `#FBBF24` | non-fatal notices, update banner |
| `Danger` | `#B91C1C` | `#F87171` | `×` errors, destructive confirms |
| `Muted` | `#6B7280` | `#9CA3AF` | secondary text, timestamps |
| `Subtle` | `#E5E7EB` | `#374151` | box borders, table rules |

- Bold for identifiers (hostnames, IDs).
- Underline for URLs only.
- Italic never (renders poorly across terminals).
- Borders: `RoundedBorder()` for plan/welcome cards, `NormalBorder()` for
  tables; no borders when piped.

## Canonical error message format

```
× <one-line summary in lowercase>
  <one-line cause, indented>

  try one of:
    <command>          <what it does>
    <command>          <what it does>

  docs: https://apigw.dev/errors/<CODE>
```

Why this shape: `×` glyph in `Danger` is scannable; cause sits under summary
for top-down reading; "try one of" is plural and action-shaped. Error code
goes in the URL only — `E_PORT_BUSY` in the terminal is noise, but searchable
in docs.

## Exit codes (stable, documented)

`0` ok · `1` generic · `2` usage/flag error · `3` config error · `4`
network/upstream error · `5` permission/privilege error · `130` SIGINT.

## Explicitly rejected patterns

- Per-keystroke validation (flickers, fights paste).
- Nested spinners.
- Clear-screen redraws for `--watch`.
- Emoji in default output. Use `✓ × ⠋ →` glyphs only.
- Implicit subcommand abbreviation (`apigw dep` ≠ `apigw deploy`).
- Reading secrets from flags or env (file or stdin only).
- Auto-update; opt-out telemetry; italics; background colors.

---

# TLS Automation Architecture for `apigw`

This brief consolidates the research that shaped our TLS automation design. It is the canonical reference for *why* `apigw tls enable` is built the way it is. Read it before touching the cert subsystem.

## Recommendation: embed `go-acme/lego` as a library

We will **embed `github.com/go-acme/lego/v4` directly** in the `apigw` binary. We considered three options:

1. **Shell out to `certbot`** — battle-tested, but adds a Python runtime dep, brittle error parsing, locale-sensitive output, shell-escaping risk, slower cold start, and forces our renewal story to live in `/etc/letsencrypt/renewal-hooks/`. We reject this: the manual flow on the Orange Pi already proved how flaky multi-process orchestration is.
2. **CertMagic** (`github.com/caddyserver/certmagic`) — higher-level, used by Caddy. Great for "always-on Go servers that terminate TLS themselves" because its sweet spot is the **on-demand TLS** path (cert obtained mid-handshake) and a `tls.Config` you hand to `http.Server`. We do **not** terminate TLS in Go — nginx does. CertMagic's storage abstraction, cluster coordination, and TLS handshake hooks are dead weight for us.
3. **lego as a library** — lower-level: a `Client`, three challenge solvers, and `Certificate.Obtain()` returning PEM bytes. That matches our job exactly: get PEM, atomically write to disk, signal nginx. Lego ships **50+ DNS-01 providers including DuckDNS**, which is the deciding factor.

This mirrors Traefik's choice (lego-based) rather than Caddy's (CertMagic-based) — and Traefik's "obtain PEM, hand to proxy" model is exactly ours. The EFF's 2024 piece argues in-process ACME is now the default for new infra; we agree.

## Reference: obtaining a cert with lego

```go
import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "github.com/go-acme/lego/v4/certificate"
    "github.com/go-acme/lego/v4/challenge/http01"
    "github.com/go-acme/lego/v4/lego"
    "github.com/go-acme/lego/v4/providers/dns/duckdns"
    "github.com/go-acme/lego/v4/registration"
)

type acmeUser struct {
    Email string
    Reg   *registration.Resource
    Key   *ecdsa.PrivateKey
}
func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.Key }

func obtainHTTP01(email, domain string, staging bool) (*certificate.Resource, error) {
    key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // account key — persist it!
    user := &acmeUser{Email: email, Key: key}

    cfg := lego.NewConfig(user)
    if staging {
        cfg.CADirURL = lego.LetsEncryptStagingCA // ALWAYS use staging in tests
    }
    cfg.Certificate.KeyType = certcrypto.EC256   // ECDSA P-256 for the cert too

    client, err := lego.NewClient(cfg)
    if err != nil { return nil, err }

    // HTTP-01 — apigw temporarily binds :80 (or proxies through nginx's /.well-known)
    if err := client.Challenge.SetHTTP01Provider(
        http01.NewProviderServer("", "80"),
    ); err != nil { return nil, err }

    reg, err := client.Registration.Register(
        registration.RegisterOptions{TermsOfServiceAgreed: true})
    if err != nil { return nil, err }
    user.Reg = reg

    return client.Certificate.Obtain(certificate.ObtainRequest{
        Domains: []string{domain}, Bundle: true,
    })
}

// DuckDNS variant — swap the solver line:
func useDuckDNS(client *lego.Client, token string) error {
    p, err := duckdns.NewDNSProviderConfig(&duckdns.Config{Token: token})
    if err != nil { return err }
    return client.Challenge.SetDNS01Provider(p)
}
```

Persist the account key (`~/.apigw/acme/account.key`) and reuse it across all certs — one account, many orders. Persist `reg.URI` so we don't re-register.

## Challenge selection matrix

| Scenario | Solver | Why |
|---|---|---|
| Public IP, real domain, port 80 reachable | **HTTP-01** | Simplest, no DNS API needed |
| Behind NAT / port 80 blocked / DuckDNS | **DNS-01** | Only viable path; required for `*.duckdns.org` |
| Wildcard `*.api.example.com` | **DNS-01** | HTTP-01 cannot do wildcards |
| Local dev | **self-signed** (no ACME) | Generate via `crypto/x509`, install in user's trust store via `mkcert`-style helper |

Skip TLS-ALPN-01 — niche, port-443-only, no advantage for us.

## Recommended nginx server block

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name api.example.com;

    # ECDSA cert from lego (fullchain = leaf + intermediates)
    ssl_certificate     /var/lib/apigw/certs/api.example.com/fullchain.pem;
    ssl_certificate_key /var/lib/apigw/certs/api.example.com/privkey.pem;

    # Mozilla Intermediate, TLS 1.2+1.3 only
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305;
    ssl_prefer_server_ciphers off;            # TLS 1.3 picks; let client choose for 1.2

    ssl_session_timeout 1d;
    ssl_session_cache shared:SSL:50m;          # ~200k sessions
    ssl_session_tickets off;                   # forward secrecy

    # NOTE: OCSP stapling intentionally OFF for Let's Encrypt certs.
    # LE turned off OCSP responders on 2025-08-06; new LE certs no longer
    # carry OCSP URLs. Keeping stapling on yields warnings and zero benefit.
    # Re-enable only if we ever support a CA that still serves OCSP.
    # ssl_stapling off;

    # HSTS — 2 years, includeSubDomains. Only emit once we are sure the user
    # really controls every subdomain on the apex; otherwise drop includeSubDomains.
    add_header Strict-Transport-Security "max-age=63072000; includeSubDomains" always;

    # HTTP/2 + sane defaults
    add_header X-Content-Type-Options "nosniff" always;

    location /.well-known/acme-challenge/ {
        root /var/lib/apigw/acme-webroot;       # for HTTP-01 renewals via webroot
    }

    location / { proxy_pass http://127.0.0.1:8080; }
}

server {
    listen 80;
    listen [::]:80;
    server_name api.example.com;
    location /.well-known/acme-challenge/ { root /var/lib/apigw/acme-webroot; }
    location / { return 301 https://$host$request_uri; }
}
```

## Renewal strategy: **systemd timer calling `apigw tls renew`**

We rejected an always-on daemon (extra failure surface, restart semantics, log rotation) and cron (no jitter, no journald integration, no `OnFailure=`). The timer runs **twice daily with randomised delay**, asks lego for any cert with <30 days remaining, and on success:

1. Writes new PEM to `/var/lib/apigw/certs/<domain>/fullchain.pem.new`
2. `rename(2)` over the live file (POSIX atomic)
3. `systemctl reload nginx` (`SIGHUP` → master re-reads files, spawns new workers, drains old)
4. Emits a structured journald event for monitoring

On failure: exponential backoff handled by the timer's `OnFailure=` unit which pings a configurable webhook. Three consecutive failures → loud alert. Never delete the old cert before the new one is verified to parse (`openssl x509 -noout`).

## Storage layout

```
/var/lib/apigw/
  acme/account.key         0600 root:root  # one account key, reused
  acme/account.json        0600 root:root  # registration URI
  certs/<domain>/
    fullchain.pem          0644 root:root
    privkey.pem            0600 root:nginx # group-readable by nginx user only
    issued_at, expires_at  0644            # human-readable metadata
```

We **do not** mirror certbot's `/etc/letsencrypt/live/<domain>/` symlink-to-archive layout — we don't need historical archives and the symlink dance breaks SELinux on some distros. We do copy certbot's filenames (`fullchain.pem`, `privkey.pem`) for muscle-memory compatibility.

## Top 10 gotchas

1. **Let's Encrypt killed OCSP on 2025-08-06.** `ssl_stapling on` with an LE cert now logs warnings and accomplishes nothing. Default to off.
2. **Account key reuse.** Generate one ECDSA key, persist it, reuse it forever. Re-registering on every cert burns the 10-accounts-per-IP-per-3-hours limit fast.
3. **Always use staging** (`lego.LetsEncryptStagingCA`) in tests/CI. Production caps at 50 certs/registered-domain/week and 5 duplicate certs/week — easy to blow in a loop.
4. **DuckDNS propagation is slow-ish.** Default `DUCKDNS_PROPAGATION_TIMEOUT=60s` is usually enough; bump to 120s if validations flake. DuckDNS only accepts one TXT value at a time — sequential orders matter (`DUCKDNS_SEQUENCE_INTERVAL=60`).
5. **HTTP-01 needs port 80, not 443.** Even if you only serve HTTPS, LE validates on :80. Don't let users firewall it off.
6. **Wildcards REQUIRE DNS-01.** No HTTP-01 path exists. If `--domain` starts with `*.`, force the DNS solver.
7. **Atomic file replacement is mandatory.** nginx workers may be mid-handshake when you swap files; `rename(2)` is atomic, `cp` is not.
8. **`nginx -s reload` reopens cert files** (it re-execs config parse → new listen contexts), but only if the *path* in config is unchanged. Don't rename the file, rename a temp file onto it.
9. **HSTS `includeSubDomains` is a trap on DuckDNS.** You don't own siblings under `*.duckdns.org`. Omit `includeSubDomains` for DuckDNS-derived hosts.
10. **Key rotation vs reuse.** Certbot reuses the cert private key by default; we will **rotate the cert key on every renewal** (lego does this when you don't pass `ObtainRequest.PrivateKey`). Tiny CPU cost, meaningful blast-radius reduction. The *account* key, however, is reused forever.

## Reference URLs

- lego library docs: <https://go-acme.github.io/lego/usage/library/>
- lego DuckDNS provider: <https://go-acme.github.io/lego/dns/duckdns/>
- lego source (DuckDNS): <https://github.com/go-acme/lego/tree/master/providers/dns/duckdns>
- CertMagic (compared, rejected): <https://github.com/caddyserver/certmagic>
- ACME challenge types: <https://letsencrypt.org/docs/challenge-types/>
- LE rate limits: <https://letsencrypt.org/docs/rate-limits/>
- LE OCSP shutdown: <https://letsencrypt.org/2024/12/05/ending-ocsp/>
- Mozilla Server-Side TLS: <https://wiki.mozilla.org/Security/Server_Side_TLS>
- nginx control signals: <https://nginx.org/en/docs/control.html>
- EFF on in-process ACME: <https://www.eff.org/deeplinks/2024/03/should-caddy-and-traefik-replace-certbot>

---

# Deploy Subsystem Architecture for `apigw deploy`

This brief consolidates the research that shaped `apigw deploy add`: take a git repo, detect the runtime, build it in a sandbox, run it under systemd, expose it via nginx, redeploy on `git push` with zero downtime. Read it before touching `modules/deploy/*`. Opinionated by design.

## Runtime detection: a deterministic decision tree, not a buildpack group

Heroku's classic buildpacks shell out to a per-language `bin/detect` script that prints a name and exits 0/1; the Node buildpack literally just checks for `package.json` at the root (see `heroku-buildpack-nodejs/bin/detect`). Cloud Native Buildpacks v3 generalises this into "groups" and "orders" with a Build Plan TOML, expanded left-to-right depth-first — powerful, but it is a graph resolver, not a detector, and assumes you ship dozens of buildpacks. Nixpacks instead runs a fixed list of providers in priority order and picks the first match. Dokku defers entirely to herokuish unless a `Dockerfile` exists (in which case Docker wins).

We will **not** import a buildpack runtime. Our user base is Node, Python, Go, and Docker — six well-known signals — so we implement a deterministic decision tree in Go:

```
1.  Dockerfile present                    -> docker        (wins over everything)
2.  go.mod present                        -> go
3.  package.json present
       and "engines.node" or has a script -> node
4.  requirements.txt OR pyproject.toml
       OR Pipfile OR poetry.lock          -> python
5.  Gemfile present                       -> ruby          (out of scope v1)
6.  Cargo.toml present                    -> rust          (out of scope v1)
7.  index.html at root OR public/ dir
       (and no other signal above)        -> static
8.  none of the above                     -> error: unsupported, ask for Dockerfile
```

Rules: **Dockerfile always wins** (Fly Launch behaves the same way) so power users can override us; static is the **fallback of last resort** so we never misclassify a Node app missing `package.json`. The detector also records *why* it picked a runtime ("matched: go.mod") and surfaces it in the dashboard — invisible heuristics are a footgun.

## Build sandboxing: systemd-run, not Docker, not bubblewrap

Build steps run untrusted code (`npm install` executes lifecycle scripts; `pip install` runs `setup.py`). Options surveyed:

- **Docker** — heaviest, requires a daemon, drags in cgroup conflicts with our systemd units, and we explicitly do not want Docker as a prerequisite.
- **Firejail** — SUID binary, Linux-only, kernel-namespace based. Convenient but the SUID surface and per-app profile model is awkward for transient build jobs.
- **bubblewrap** — minimal, unprivileged (user namespaces). The project README is explicit that "whatever program constructs the command-line arguments… is responsible for defining its own security model" — you are building your own sandbox policy. Great if we needed it, but we'd be reimplementing what systemd already gives us.
- **systemd-nspawn** — container-grade, too heavy, requires `/var/lib/machines`.
- **systemd-run with hardening directives** — *no extra dependency*, same hardening primitives we already use for the runtime service, integrates with `journald` for log streaming, gets cgroup resource limits for free.

**Recommendation (confirmed): `systemd-run --scope --uid=apigw-build --slice=apigw-builds.slice` plus the full hardening directive set.** The build runs in a transient scope inheriting our `.slice` budget. systemd's `systemd-analyze security` will score it the same as a long-running unit. If we later need stricter filesystem isolation (e.g. running customer code at scale), we wrap the same command in `bwrap --ro-bind / / --tmpfs /tmp` *inside* the systemd-run scope — defence in depth, not replacement.

Build user: a dedicated unprivileged `apigw-build` system user, **never** root, **never** the deploy user (which owns the service state directory). Build workdir is a fresh `/var/lib/apigw/builds/<deploy>/<sha>/` so a poisoned build cannot mutate the running release. Caches (`~/.npm`, `~/.cache/pip`, `$GOPATH/pkg/mod`) live in `CacheDirectory=apigw/<deploy>` so they survive across builds but stay scoped per deployment.

## Process supervision: systemd, end of story

s6, runit and PM2 are all viable on paper, but our targets are Debian 12 / Ubuntu 22.04+, where systemd is PID 1 and `journald` is already capturing every byte of stdout/stderr. Reinventing supervision means losing socket activation, cgroup limits, `systemd-analyze security`, `journalctl`, watchdog, and `Restart=` semantics that ops people already know. The legacy bash installer used PM2; we delete that.

We provision **one template unit per deployment** via `apigw-deploy@.service` plus an `apigw-deploy@.socket` for socket activation. `systemctl start apigw-deploy@myapp.service` brings up a single instance; the `%i` specifier carries the deployment name into all paths.

## Gold-standard hardened unit template

```ini
# /etc/systemd/system/apigw-deploy@.service
[Unit]
Description=apigw deployment %i
After=network-online.target apigw-deploy@%i.socket
Requires=apigw-deploy@%i.socket
PartOf=apigw.target

[Service]
Type=notify                       # app calls sd_notify(READY=1); apigw waits for it
NotifyAccess=main
WatchdogSec=30s                   # app must ping; otherwise restart

# --- identity ---
User=apigw-run                    # static user; we do NOT use DynamicUser= here
Group=apigw-run                   # because StateDirectory chowning on every restart
                                  # is noisy in journald and our state IS persistent

# --- paths (systemd creates/chowns these) ---
StateDirectory=apigw/%i           # /var/lib/apigw/<name>  — release symlink lives here
LogsDirectory=apigw/%i            # /var/log/apigw/<name>
ConfigurationDirectory=apigw/%i   # /etc/apigw/<name>      — env file lives here
RuntimeDirectory=apigw/%i         # /run/apigw/<name>
WorkingDirectory=/var/lib/apigw/%i/current
EnvironmentFile=-/etc/apigw/%i/app.env   # leading dash = optional
ExecStart=/var/lib/apigw/%i/current/.apigw/start.sh

# --- filesystem isolation ---
ProtectSystem=strict              # / is read-only except StateDir/LogsDir/etc
ProtectHome=yes                   # /home, /root invisible
PrivateTmp=yes                    # private /tmp namespace; cleaned on stop
PrivateDevices=yes                # only /dev/null, /dev/zero, /dev/random
ReadWritePaths=                   # default empty; StateDirectory is RW automatically
ReadOnlyPaths=/etc

# --- kernel isolation ---
ProtectKernelTunables=yes         # /proc/sys, /sys read-only
ProtectKernelModules=yes          # block module load
ProtectKernelLogs=yes             # block /dev/kmsg
ProtectControlGroups=yes          # /sys/fs/cgroup read-only
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible             # cannot see other users' processes
ProcSubset=pid                    # only /proc/<pid>, no /proc/cpuinfo etc

# --- privilege isolation ---
NoNewPrivileges=yes               # setuid binaries cannot escalate
CapabilityBoundingSet=            # empty — drop ALL capabilities
AmbientCapabilities=
RestrictSUIDSGID=yes

# --- namespace/personality ---
RestrictNamespaces=yes            # cannot create user/pid/net/etc namespaces
LockPersonality=yes               # cannot switch ABI (x86 <-> x86_64)
RestrictRealtime=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX   # no AF_NETLINK, AF_PACKET
PrivateUsers=no                   # leave OFF — breaks listening on :<1024 if ever needed

# --- syscall filter ---
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources @debug @mount @swap @reboot @raw-io
SystemCallErrorNumber=EPERM
SystemCallArchitectures=native

# NOTE: MemoryDenyWriteExecute=yes is INTENTIONALLY OMITTED.
# Node.js (V8 JIT), Python (ctypes, some C extensions), and the JVM all
# require W+X mappings. Enable per-runtime where safe (Go, static binaries).

# --- resource limits (defaults; user-overridable via apigw deploy set-limits) ---
CPUQuota=50%
MemoryHigh=384M                   # soft limit — reclaim aggressively
MemoryMax=512M                    # hard limit — OOM kill
TasksMax=256
IOWeight=100
LimitNOFILE=4096

# --- restart policy ---
Restart=on-failure
RestartSec=5s
StartLimitIntervalSec=120s
StartLimitBurst=5                 # 5 failures in 2 min => give up, surface to user

[Install]
WantedBy=multi-user.target
```

Every directive above changes the `systemd-analyze security` score; targeting **≤ 2.0 ("OK")** is realistic for a Go/static-binary app, **≤ 3.5** for Node/Python (because `MemoryDenyWriteExecute` is off).

Companion socket unit, for zero-downtime restart:

```ini
# /etc/systemd/system/apigw-deploy@.socket
[Socket]
ListenStream=127.0.0.1:%I-port    # we resolve port via drop-in; see deploy flow
Accept=no                         # one socket, passed to one service instance
NoDelay=yes
ReusePort=yes

[Install]
WantedBy=sockets.target
```

## Zero-downtime deploy sequence

We choose **nginx upstream switch** as the primary strategy and **socket activation** as the belt-and-braces option for stateful TCP apps. Kamal does container-swap via `kamal-proxy` polling `/up`; Fly does Firecracker VM swap. Both are overkill on a single host.

```
t0  webhook arrives (HMAC verified)            -> enqueue job, return 202 in <100ms
t1  worker dequeues, takes per-deploy lock     -> only one build per app at a time
t2  if commit SHA == current release SHA       -> skip (idempotent)
t3  git fetch + checkout into builds/<sha>/    -> via go-git, shallow depth=1
t4  detect runtime                             -> writes detection.json into builds/<sha>/
t5  build in sandbox (systemd-run)             -> logs streamed via sdjournal
t6  health-probe new instance on port N+1      -> GET /health, 20× with 500ms backoff
t7  rewrite nginx upstream: 127.0.0.1:N+1      -> atomic file write + nginx -s reload
t8  flip "current" symlink                     -> /var/lib/apigw/<app>/current -> <sha>
t9  systemctl stop apigw-deploy@<app>@<oldport>  (drain 30s, then SIGTERM, 10s SIGKILL)
t10 emit DeploySucceeded event to event hub
```

If t6 or t7 fails, we **never** flip the symlink: the old release keeps serving and the user sees `DeployFailed` with the build log URL. nginx reload is graceful (it forks workers, old workers finish in-flight requests). Drain timeout is configurable per-deploy via `StopGracePeriodSec=` plus `TimeoutStopSec=` overrides.

## Git deploy: webhook-only, idempotent, single-flight

We do **not** ship a bare repo + post-receive hook (Heroku-style `git push apigw main`). GitHub-first means HTTPS webhook is the natural interface. Rules:

- **Respond in <2s with 202 Accepted**, processing async. GitHub's documented timeout is 10s; we aim for a 10× margin. Heavy work goes onto an in-process job queue (`go-co-op/gocron`-style worker pool, persisted to BoltDB so a restart doesn't drop jobs).
- **HMAC verification** with `X-Hub-Signature-256` using `crypto/hmac` and `subtle.ConstantTimeCompare`. Reject before parsing the body.
- **Single-flight per deployment**: `sync.Mutex` keyed by deploy name, *not* global. Two pushes to different apps build in parallel; two pushes to the same app serialise, and the second one supersedes any queued-but-not-yet-started job for the same deploy (debounce on commit SHA).
- **Idempotency by commit SHA**: if `git rev-parse HEAD` after fetch matches the currently-deployed SHA, skip the build and emit `DeploySkipped`.
- **Build cache survives across deploys** via `CacheDirectory=`; we invalidate selectively on lockfile change (`package-lock.json`, `go.sum`, `requirements.txt`) using `fsnotify` plus a content hash stored in `cache.manifest`.

## Logging: journald is already the event hub

stdout/stderr from `ExecStart=` lands in `journald` automatically. We read it back via `coreos/go-systemd/v22/sdjournal` (D-Bus-free native API; lower overhead than spawning `journalctl -f --output=json`). The reader is filtered by `_SYSTEMD_UNIT=apigw-deploy@<name>.service` and tails forward, emitting `LogLine` events onto the event hub WebSocket.

`/etc/systemd/journald.conf.d/apigw.conf` caps disk usage:

```ini
[Journal]
SystemMaxUse=2G
SystemKeepFree=1G
MaxRetentionSec=2week
```

Per-deploy log rotation lives in `LogsDirectory=` only for files the app writes directly (we discourage this — pipe to stdout).

## Secrets

`EnvironmentFile=-/etc/apigw/%i/app.env` with mode `0600`, owner `apigw-run`. Users edit via `apigw deploy env set FOO=bar`, which writes atomically (`rename(2)`) and triggers `systemctl restart`. On Debian 12+ we additionally support **systemd-creds** for encrypted-at-rest secrets: `LoadCredentialEncrypted=db-password:/etc/apigw/<app>/creds/db-password.cred`, decrypted by TPM2 or the host key into a tmpfs-only path the app reads via `$CREDENTIALS_DIRECTORY`. `.env` files in the repo are loaded by the build step but we **warn loudly** if one is committed (regex on `git ls-files`).

## Tools & libraries (canonical choices)

- `github.com/go-git/go-git/v5` — pure-Go git clone/fetch, no shell-out to `git`.
- `github.com/coreos/go-systemd/v22/dbus` — start/stop/reload units; read unit properties.
- `github.com/coreos/go-systemd/v22/sdjournal` — tail journald natively.
- `github.com/coreos/go-systemd/v22/daemon` — `sd_notify(READY=1)`, watchdog ping.
- `github.com/fsnotify/fsnotify` — invalidate build cache on lockfile change.
- `go.etcd.io/bbolt` — persistent job queue (single-file, no daemon).
- `golang.org/x/crypto/nacl/secretbox` — encrypted-at-rest secret blobs where systemd-creds is unavailable.
- `github.com/google/go-github/v62/github` — webhook payload parsing only; we still verify HMAC ourselves.
- `github.com/stretchr/testify` — unit tests; integration tests run real `systemd-run --user`.

## Anti-patterns we will not commit

1. **Building as root.** A malicious `postinstall` script becomes a full host compromise. Always `User=apigw-build`, never `0`.
2. **In-place builds inside `current/`.** A failed build mid-`npm ci` leaves a corrupt release serving traffic. Always build in a fresh `<sha>/` dir and flip the symlink only after health-check passes.
3. **Holding nginx config in memory.** Always write the new upstream to a temp file, `rename(2)` into place, then `nginx -s reload`. `nginx -t` first; refuse to reload an invalid config.
4. **Synchronous webhook handling.** Doing the build in the HTTP handler will trip GitHub's 10s timeout, cause retries, cause duplicate deploys. Always 202 + enqueue.
5. **Trusting GitHub IPs instead of HMAC.** IP allowlists drift; HMAC with constant-time compare is the spec. Reject unsigned payloads with 401, *not* 400 (don't leak that the signature was malformed vs missing).
6. **`MemoryDenyWriteExecute=yes` blindly.** Breaks Node, Python ctypes, JVM, anything JIT. Off by default, opt-in for Go/static binaries.
7. **`DynamicUser=yes` for stateful deploys.** UID churn means systemd chowns `StateDirectory` on every restart and journald log ownership flaps. Use it only for the build scope, not the runtime.
8. **Committing `.env` to git.** We scan `git ls-files` on every deploy and warn; we do not silently fix.

## References

- CNB v3 detect spec: <https://github.com/buildpacks/spec/blob/main/buildpack.md>
- Heroku Node detect: <https://github.com/heroku/heroku-buildpack-nodejs/blob/main/bin/detect>
- Nixpacks providers: <https://nixpacks.com/docs/providers>
- Dokku herokuish builder: <https://dokku.com/docs/deployment/builders/herokuish-buildpacks/>
- Fly Launch: <https://fly.io/docs/launch/>
- systemd.exec(5) directives: <https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html>
- systemd-analyze security: <https://www.freedesktop.org/software/systemd/man/latest/systemd-analyze.html>
- Lennart on socket activation: <http://0pointer.de/blog/projects/socket-activation.html>
- Lennart on DynamicUser=: <https://0pointer.net/blog/dynamic-users-with-systemd.html>
- bubblewrap: <https://github.com/containers/bubblewrap>
- firejail: <https://github.com/netblue30/firejail>
- Kamal proxy: <https://kamal-deploy.org/docs/configuration/proxy/>
- coreos/go-systemd: <https://github.com/coreos/go-systemd>
- GitHub webhook best practices: <https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks>

---

# Live Streaming Architecture for `apigw`

> Scope: how deploy build logs, service-status changes, webhook activity, TLS
> expiry, and nginx access logs reach the **dashboard** and the **CLI** in
> sub-100 ms, without polling, without manual refresh, on a Raspberry Pi 4.

## 1. Topology

One in-process event hub fans producers out to N subscribers. The CLI and the
HTTP/SSE endpoint are just two flavours of subscriber against the same hub —
no duplicate plumbing, no second event bus.

```
              producers                       hub                 subscribers
   +----------------------------+   +----------------------+   +----------------+
   | exec.Cmd (npm install)     |-->|                      |-->| SSE handler    |--> browser EventSource
   | tail -f /var/log/nginx     |-->|   Hub                |-->| SSE handler    |--> browser (2nd tab)
   | systemd journal (sdjournal)|-->|  - per-topic         |-->| CLI streamer   |--> apigw deploy logs -f
   | webhook receiver (HTTP)    |-->|    ring buffer 1024  |-->| CLI streamer   |--> apigw status --watch
   | TLS expiry ticker (1/hr)   |-->|  - per-sub bounded   |   | file writer    |--> /var/log/apigw/*.log
   | deploy state machine       |-->|    channel (cap 256) |   +----------------+
   +----------------------------+   +----------------------+
```

Topics are strings: `deploy.<app>.stdout`, `deploy.<app>.state`, `webhook.recv`,
`tls.expiry`, `nginx.access`, `svc.<unit>.state`. Subscribers pick the topics
they want. The hub is a single Go package, no external broker, no Redis.

## 2. Transport choice: SSE, confirmed

| Option | Pro | Con | Verdict |
|---|---|---|---|
| **SSE** | Plain HTTP/1.1+, auto-reconnect built into `EventSource`, `Last-Event-ID` replay, traverses every proxy, ~20 LoC in Go using `http.Flusher`. GitHub Actions live log UI uses it. | Text-only, 6-conn/origin cap on HTTP/1.1 (lifts to ~100 on HTTP/2). | **Picked.** |
| WebSocket | Bidirectional, binary. Used by [Grafana Loki `/loki/api/v1/tail`](https://grafana.com/docs/loki/latest/reference/loki-http-api/). | Separate protocol, corporate proxies sometimes strip `Upgrade`, more framing code, no native browser auto-reconnect. We don't need browser->server messages on the log channel. | Rejected. |
| HTTP/2 raw streaming | Multiplexes nicely. [`kubectl logs -f`](https://github.com/kubernetes/kubernetes/issues/50857) reads chunked HTTP from the apiserver. | Uneven browser support for reading streams, no replay semantics, need our own reconnect logic in JS. | Rejected for browser; used as an implementation detail of the CLI transport. |
| gRPC-web | Schema. | Build complexity, Envoy/proxy, browser quirks. Overkill for a single-box installer. | Rejected. |

**Evidence from neighbours**

- **GitHub Actions** uses SSE for its live job log viewer — same shape as our deploy build log.
- **Fly.io** uses [NATS internally](https://fly.io/docs/monitoring/logs-api-options/) but [`fly-apps/natstream`](https://github.com/fly-apps/natstream) re-exposes it as SSE to browsers — confirming SSE is the right *edge* transport even when the *internal* bus is something heavier.
- **Loki picked WS** but their need is the opposite of ours: thousands of concurrent multi-tenant tails over a query proxy. We have one user and <=10 tabs.

The 6-connection limit is real on HTTP/1.1 but we serve the dashboard over
HTTP/2 (already true because nginx terminates TLS in front of `apigw`). HTTP/2
lifts the per-origin cap to ~100 concurrent streams — well beyond our "5
widgets x 3 tabs" worst case. We still **multiplex topics on a single SSE
connection per tab** as belt-and-braces.

## 3. The hub (~100 LoC, ours)

We do **not** pull in [Watermill](https://github.com/ThreeDotsLabs/watermill)
(designed for Kafka/NATS), nor [cskr/pubsub](https://github.com/cskr/pubsub)
(close but doesn't carry monotonic IDs needed for `Last-Event-ID` replay). The
pattern is small enough to own, matching what Caddy and HashiCorp Nomad do
internally for their event streams.

```go
package hub

import (
    "sync"
    "sync/atomic"
)

type Event struct {
    ID    uint64 // monotonic, per-hub
    Topic string
    Ts    int64  // unix nanos
    Type  string // "stdout","state","webhook",...
    Data  []byte // JSON, single line
}

type sub struct {
    topics map[string]struct{}
    ch     chan Event // bounded, e.g. cap 256
    drops  atomic.Uint64
}

type Hub struct {
    mu     sync.RWMutex
    subs   map[*sub]struct{}
    rings  map[string]*ring // per-topic, capacity 1024
    nextID atomic.Uint64
}

func (h *Hub) Publish(topic, typ string, data []byte) {
    e := Event{ID: h.nextID.Add(1), Topic: topic, Ts: nowNanos(), Type: typ, Data: data}
    h.mu.RLock()
    h.rings[topic].push(e) // ring is lock-free SPSC; one publisher per topic
    for s := range h.subs {
        if _, ok := s.topics[topic]; !ok {
            continue
        }
        select {
        case s.ch <- e: // happy path
        default:
            // backpressure: drop oldest in sub's queue, push newest
            select { case <-s.ch: default: }
            select {
            case s.ch <- e:
                s.drops.Add(1)
            default:
                s.drops.Add(1)
            }
        }
    }
    h.mu.RUnlock()
}

func (h *Hub) Subscribe(topics []string, sinceID uint64) (<-chan Event, func()) {
    s := &sub{topics: setOf(topics), ch: make(chan Event, 256)}
    // replay backlog newer than sinceID, in order, before live events
    go h.replay(s, sinceID)
    h.mu.Lock(); h.subs[s] = struct{}{}; h.mu.Unlock()
    return s.ch, func() {
        h.mu.Lock(); delete(h.subs, s); h.mu.Unlock()
        close(s.ch)
    }
}
```

**Backpressure choice: drop-oldest-from-subscriber-queue.** Kafka blocks
producers on consumer lag; NATS Core drops with `slow consumer` and
disconnects; Redis Streams blocks at `MAXLEN`. For an interactive log viewer
**freshness beats history** — a paused-tab subscriber must not stall the
deploy. After 1024 cumulative drops on one subscriber we close its channel
(the client reconnects with its `Last-Event-ID` and re-syncs from the ring).

**Ring sizing.** 1024 events x ~256 B average JSON payload ~= 256 KiB per
topic. With ~10 topics that's 2.5 MiB resident — trivial on a Pi 4 (4 GiB
RAM). 1024 is ~2 s of headroom at 500 lines/sec, which covers `npm install`
peak chatter. Configurable per-topic.

## 4. SSE handler with `Last-Event-ID`

```go
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok { http.Error(w, "no flusher", 500); return }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache, no-transform")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no") // nginx: do not buffer

    var sinceID uint64
    if v := r.Header.Get("Last-Event-ID"); v != "" {
        sinceID, _ = strconv.ParseUint(v, 10, 64)
    }
    topics := r.URL.Query()["topic"] // ?topic=deploy.foo.stdout&topic=webhook.recv

    ch, cancel := s.hub.Subscribe(topics, sinceID)
    defer cancel()

    io.WriteString(w, "retry: 3000\n\n") // override default reconnect interval
    flusher.Flush()

    heartbeat := time.NewTicker(15 * time.Second)
    defer heartbeat.Stop()
    ctx := r.Context()

    for {
        select {
        case <-ctx.Done():
            return
        case <-heartbeat.C:
            io.WriteString(w, ": keepalive\n\n") // comment line, ignored by client
            flusher.Flush()
        case e, ok := <-ch:
            if !ok { return }
            // id MUST precede data; data MUST be single-line JSON
            fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, e.Data)
            flusher.Flush()
        }
    }
}
```

Spec points the [WHATWG SSE](https://html.spec.whatwg.org/multipage/server-sent-events.html)
section enforces:

- `Content-Type: text/event-stream` is mandatory — browser fails the connection otherwise.
- `id:` is what `EventSource` stores and replays in `Last-Event-ID` on reconnect.
- Multiple `data:` lines are joined with `\n` — so we keep `data` as a single JSON line and let the client `JSON.parse`.
- `:`-prefixed comments are valid no-ops — that's our heartbeat. 15 s beats every known idle-proxy timeout (nginx 60 s default, Cloudflare 100 s).
- `X-Accel-Buffering: no` defeats nginx's response buffering — a classic gotcha.

We use `event:` (not just `data:`) because subscribers want to dispatch on
type without parsing the JSON for every event — `es.addEventListener("stdout", ...)`
is cleaner than a single `onmessage` that re-dispatches.

## 5. Dashboard JS

```js
function connect() {
  const url = "/api/stream?topic=deploy.app1.stdout&topic=webhook.recv";
  const es = new EventSource(url, { withCredentials: true });

  es.addEventListener("stdout",  (ev) => logView.append(JSON.parse(ev.data)));
  es.addEventListener("state",   (ev) => badge.set(JSON.parse(ev.data)));
  es.addEventListener("webhook", (ev) => feed.push(JSON.parse(ev.data)));

  es.onerror = () => {
    // EventSource auto-reconnects using lastEventId; we just log.
    console.warn("SSE reconnecting");
  };
}
connect();

// DOM batching - accumulate, flush in rAF, append in one fragment.
const queue = [];
function append(line) {
  queue.push(line);
  if (queue.length === 1) requestAnimationFrame(flush);
}
function flush() {
  const frag = document.createDocumentFragment();
  for (const l of queue) frag.appendChild(renderLine(l));
  logEl.appendChild(frag);
  queue.length = 0;
  if (pinnedToBottom) logEl.scrollTop = logEl.scrollHeight;
}
logEl.addEventListener("scroll", () => {
  pinnedToBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 4;
});
```

[`EventSource` cannot set custom headers](https://news.ycombinator.com/item?id=30313515)
— we authenticate via the existing session cookie (`withCredentials: true`). If
we ever need a bearer token we'll switch the client to
[`@microsoft/fetch-event-source`](https://medium.com/pon-tech-talk/extend-the-usage-of-the-eventsource-api-with-microsoft-fetch-event-source-a5c83ff95964),
not roll our own. Auto-scroll uses the Discord/Slack rule: pin while within 4 px
of bottom, freeze on scroll-up.

## 6. Performance budget

| Metric | Target | How we measure |
|---|---|---|
| Disk-write -> browser DOM | <100 ms p99 | Stamp `Ts` in publisher, JS records `performance.now()` on event, hub exposes `/debug/latency` |
| Sustained throughput | 1000 ev/s, zero publisher block | `go test -bench BenchmarkHubPublish -benchtime=10s` |
| CPU idle (Pi 4, 1 stream, 10 ev/s) | <0.1 % | `pidstat -p $(pgrep apigw) 1 60` |
| RAM resident | <30 MiB w/ 10 topics x 1024 ring | `/proc/$pid/status VmRSS` |
| Reconnect after `kill -HUP` | <3 s, zero gap on `Last-Event-ID` | integration test: kill server, assert no missing `id:` |
| Concurrent SSE conns | 200 | `hey -n 200 -c 200 -t 0` against `/api/stream` |

Load test: a Go test spawns N goroutines each emitting an `npm install`
lookalike (1 line/ms for 5 s); a second set of goroutines opens SSE
connections and verifies monotonic `id` with no gaps.

## 7. Library shortlist (and what we write)

| Use | Library | Why |
|---|---|---|
| File tail w/ rotation | [`github.com/nxadm/tail`](https://github.com/nxadm/tail) | Maintained fork of `hpcloud/tail`. `Config{Follow: true, ReOpen: true}` handles logrotate rename and truncate. |
| Inotify (rotation hint) | [`github.com/fsnotify/fsnotify`](https://github.com/fsnotify/fsnotify) | Belt-and-braces alongside `nxadm/tail` for non-standard rotators. |
| Journal | [`github.com/coreos/go-systemd/v22/sdjournal`](https://pkg.go.dev/github.com/coreos/go-systemd/v22/sdjournal) | Native; faster than shelling out to `journalctl -f --output=json`. |
| Process stdout | `os/exec` + `bufio.Scanner` on `StdoutPipe`/`StderrPipe` | Stdlib; tee to file + ring + SSE. |
| Ring buffer | **hand-rolled, ~40 LoC** | Per-topic SPSC, lock-free; `Workiva/go-datastructures` is overkill. |
| Pub/sub hub | **ours** | Need monotonic IDs + replay; no good off-the-shelf match. `cskr/pubsub` is close but lacks IDs/replay. |
| CLI streaming | `net/http` w/ chunked + `bufio.Scanner` | Same `/api/stream` endpoint, content-negotiated to `application/x-ndjson` for the CLI. |
| Front-end | vanilla JS + `EventSource` | HTMX SSE extension considered; vanilla is fewer moving parts for v1. |

## 8. Gotchas (preempted)

1. **nginx buffers SSE** — fix with `X-Accel-Buffering: no` on the response *and* `proxy_buffering off;` in the `location` block.
2. **`npm install` emits `\r` progress bars** — strip CR-runs server-side before publish (`bytes.LastIndexByte(line, '\r')`); preserving them DoS's the DOM.
3. **Log rotation truncate-vs-rename** — `nxadm/tail` with `ReOpen: true` handles both; this is `tail -F` semantics, not `tail -f`.
4. **`EventSource` reconnect storm** on server restart — send `retry: 3000\n\n` once on connect; jitter server-side if many tabs reconnect simultaneously.
5. **HTTP/1.1 6-conn cap** — serve dashboard over HTTP/2; also multiplex topics over one SSE connection.
6. **Cookie-auth + SSE + CORS** — `withCredentials: true` requires `Access-Control-Allow-Credentials: true`; wildcard `Access-Control-Allow-Origin: *` is forbidden in that combo.
7. **Slow tab in background** — browsers throttle timers but **not** EventSource delivery. Drop-oldest means a 10-min-away tab snaps to current on focus.
8. **Server restart loses in-flight events** — acceptable: the producers (deploy, nginx, journal) restart too. Document it; don't try to persist.

## Sources (live-streaming)

- WHATWG SSE spec: <https://html.spec.whatwg.org/multipage/server-sent-events.html>
- Grafana Loki HTTP API (`/loki/api/v1/tail`, WebSocket): <https://grafana.com/docs/loki/latest/reference/loki-http-api/>
- Loki Live Tailing blog: <https://grafana.com/blog/lokis-path-to-ga-live-tailing/>
- Fly Logs API options (NATS internally): <https://fly.io/docs/monitoring/logs-api-options/>
- `fly-apps/natstream` — NATS-to-SSE bridge: <https://github.com/fly-apps/natstream>
- Kubernetes chunked-stream behaviour, issue #50857: <https://github.com/kubernetes/kubernetes/issues/50857>
- `cskr/pubsub`: <https://github.com/cskr/pubsub>
- `nxadm/tail`: <https://github.com/nxadm/tail>
- `fsnotify/fsnotify`: <https://github.com/fsnotify/fsnotify>
- `coreos/go-systemd/sdjournal`: <https://pkg.go.dev/github.com/coreos/go-systemd/v22/sdjournal>
- MDN EventSource.withCredentials: <https://developer.mozilla.org/en-US/docs/Web/API/EventSource/withCredentials>
- HN — custom headers on EventSource: <https://news.ycombinator.com/item?id=30313515>
- `@microsoft/fetch-event-source` workaround: <https://medium.com/pon-tech-talk/extend-the-usage-of-the-eventsource-api-with-microsoft-fetch-event-source-a5c83ff95964>
- Real-time log viewer with SSE (DEV): <https://dev.to/polliog/building-a-real-time-log-viewer-with-server-sent-events-and-svelte-5-13dd>

---

# Security & Distribution

This brief covers the supply-chain, signing, secret-storage, and
privilege-isolation choices for `apigw`. We aim **above** the bar that
`gh` and `caddy` set today — `gh`'s `.goreleaser.yml` uses neither cosign
nor SBOM; Caddy's stock systemd unit omits `NoNewPrivileges` and
`ProtectHome`. Our floor is their ceiling.

## 1. Build & release — goreleaser + SLSA Level 3

```yaml
# .goreleaser.yaml
version: 2

before:
  hooks: [go mod tidy, go mod download]

builds:
  - id: apigw
    main: ./cmd/apigw
    env:    [CGO_ENABLED=0]
    flags:  [-trimpath]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.CommitDate}}
      - -extldflags=-static
    mod_timestamp: '{{ .CommitTimestamp }}'    # reproducible build
    goos:   [linux, darwin]
    goarch: [amd64, arm64, arm]
    goarm:  ['7']
    ignore:
      - {goos: darwin, goarch: arm}

archives:
  - format: tar.gz
    name_template: "apigw_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files: [LICENSE, README.md, completions/*, man/*]

checksum:
  name_template: 'checksums.txt'
  algorithm: sha256

sboms:                         # syft generates SPDX per archive
  - artifacts: archive
    documents: ["${artifact}.sbom.spdx.json"]

signs:                         # keyless cosign on the checksums file
  - cmd: cosign
    signature: "${artifact}.sig"
    certificate: "${artifact}.pem"
    args:
      - sign-blob
      - --output-signature=${signature}
      - --output-certificate=${certificate}
      - --yes
      - ${artifact}
    artifacts: checksum

nfpms:                         # .deb + .rpm
  - vendor: apigw
    license: Apache-2.0
    formats: [deb, rpm]
    bindir: /usr/bin
    contents:
      - {src: dist/systemd/apigw-dashboard.service, dst: /lib/systemd/system/apigw-dashboard.service}
      - {src: completions/apigw.bash, dst: /usr/share/bash-completion/completions/apigw}
      - {src: completions/apigw.zsh,  dst: /usr/share/zsh/site-functions/_apigw}
      - {src: completions/apigw.zsh,  dst: /usr/share/zsh/vendor-completions/_apigw}  # Debian quirk

brews:
  - repository: { owner: apigw, name: homebrew-tap }
    license: Apache-2.0
    test: |
      system "#{bin}/apigw version"

release:
  draft: true
  prerelease: auto
```

Pair with the SLSA L3 reusable workflow
(`slsa-framework/slsa-github-generator/.github/workflows/builder_go_slsa3.yml`).
Its `intoto.jsonl` provenance is non-forgeable — bound to our GitHub
Actions OIDC identity at sign time. User-facing verification:

```
cosign verify-blob \
  --certificate-identity-regexp 'github.com/apigw/apigw/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate checksums.txt.pem \
  --signature   checksums.txt.sig \
  checksums.txt
```

## 2. Install script (the safe `curl | sudo bash`)

Keep it under ~80 lines. `tailscale.com/install.sh` discipline (`main()`
wrap so a partial download can't execute) plus `rustup-init.sh` `set -u`
patterns:

```sh
#!/bin/sh
set -eu
main() {
  VERSION="${APIGW_VERSION:-v1.0.0}"   # pinned default
  REPO="apigw/apigw"
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m); case "$ARCH" in
    x86_64)         ARCH=amd64 ;;
    aarch64|arm64)  ARCH=arm64 ;;
    armv7l)         ARCH=arm ;;
  esac

  TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
  TARBALL="apigw_${VERSION#v}_${OS}_${ARCH}.tar.gz"

  for f in "$TARBALL" checksums.txt checksums.txt.sig checksums.txt.pem; do
    curl -fsSL --tlsv1.2 --proto '=https' "${BASE}/${f}" -o "${TMP}/${f}"
  done

  if command -v cosign >/dev/null; then
    cosign verify-blob \
      --certificate "${TMP}/checksums.txt.pem" \
      --signature   "${TMP}/checksums.txt.sig" \
      --certificate-identity-regexp "https://github.com/${REPO}/.github/workflows/release.yml@.*" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      "${TMP}/checksums.txt"
  else
    echo "warning: cosign not installed — falling back to sha256 verification only"
  fi

  (cd "$TMP" && grep " ${TARBALL}\$" checksums.txt | sha256sum -c -)
  tar -xzf "${TMP}/${TARBALL}" -C "$TMP"
  install -m 0755 "${TMP}/apigw" /usr/local/bin/apigw
  echo "Installed $(/usr/local/bin/apigw version)"
}
main "$@"
```

Pattern: shim downloads + verifies + installs the binary; **all real
install work lives in `apigw install`**, which is testable, observable,
and version-controlled — not bash.

## 3. Webhook HMAC (the only correct shape)

```go
func verifySignature(secret, body []byte, header string) bool {
    const prefix = "sha256="
    if !strings.HasPrefix(header, prefix) {
        return false                          // fail closed; no signature → reject
    }
    got, err := hex.DecodeString(header[len(prefix):])
    if err != nil {
        return false
    }
    mac := hmac.New(sha256.New, secret)
    mac.Write(body)
    return hmac.Equal(got, mac.Sum(nil))       // CONSTANT TIME — never use == or bytes.Equal
}
```

Replay protection: track `X-GitHub-Delivery` UUIDs in a bounded LRU for
10 minutes; reject duplicates. GitHub doesn't put a timestamp in the
payload by default — UUID uniqueness is the practical substitute.

## 4. Secrets storage matrix

| Secret | Default storage | Opt-in upgrade | Rationale |
| --- | --- | --- | --- |
| Webhook HMAC secret | `/etc/apigw/webhooks/<app>.secret` `0600 root:apigw` | `systemd-creds` encrypted, via `LoadCredentialEncrypted=` | small, read once at startup |
| DuckDNS / DNS API token | `/etc/apigw/dns.env` `0600 root:apigw` | `systemd-creds` | long-lived; never logged; rotate quarterly |
| TLS private keys | `/var/lib/apigw/certs/<domain>/privkey.pem` `0600 root:root` ACL'd to nginx group | leave alone | lifecycle owned by ACME client |
| Deploy `.env` (DB creds etc.) | `/etc/apigw/apps/<app>.env` `0600 root:<app>` | `sops` + `age` if checked into git | app-boundary secrets |

`sops`+`age` is right when the user wants config in git; `systemd-creds`
is right for TPM-bound at-rest encryption on the host. Vault/keyring
are over-budget.

## 5. `systemd-creds` primer (opt-in)

Available on systemd ≥ 250 (ubiquitous on Ubuntu 22.04+ / Debian 12+).
Encrypts a blob with a key derived from TPM2 + host secret; ciphertext is
safe in `/etc` or even committed to config management. One-time encrypt:

```
systemd-creds encrypt --name=webhook plaintext.txt /etc/apigw/creds/webhook.cred
```

Unit reads via `LoadCredentialEncrypted=webhook:/etc/apigw/creds/webhook.cred`;
the service reads from `$CREDENTIALS_DIRECTORY/webhook` — plaintext never
touches disk. Detect support with `systemd-creds has-tpm2`.

## 6. Reference `apigw-dashboard.service`

```ini
[Unit]
Description=apigw dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=apigw
Group=apigw
ExecStart=/usr/bin/apigw dashboard serve
Restart=on-failure
RestartSec=2s

# Capabilities: bind 443 without root
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

# Filesystem
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true   # safe — Go has no JIT
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources

# Managed dirs (systemd creates and chowns)
ConfigurationDirectory=apigw
ConfigurationDirectoryMode=0750
StateDirectory=apigw
StateDirectoryMode=0750
LogsDirectory=apigw

# Encrypted credentials (opt-in)
LoadCredentialEncrypted=webhook:/etc/apigw/creds/webhook.cred

[Install]
WantedBy=multi-user.target
```

Notice `MemoryDenyWriteExecute=true` here — safe for the Go dashboard,
**not** safe in the deploy-user units (V8/Python/JIT). Different units,
different hardening profiles.

## 7. Privilege model

Two surfaces:

- **Root-only mutators**: `apigw install`, `apigw api add/remove`,
  `apigw tls enable/renew`, `apigw deploy add`. Invoked via `sudo`. They
  touch `/etc/`, `/lib/systemd/system/`, install packages.
- **Read-only / per-user**: `apigw status`, `apigw logs`, `apigw deploy list`.
  Work for any member of group `apigw`.

The dashboard daemon runs as `apigw:apigw` with `AmbientCapabilities=CAP_NET_BIND_SERVICE`
to bind 443 without root — same trick Caddy uses. **Never drop privileges
mid-process in Go.** The runtime's threading model makes `setuid(2)`
racy. Polkit is overkill for our user base.

## 8. Self-update

`apigw upgrade` (explicit, not automatic), built on `minio/selfupdate`:
download → cosign verify (`sigstore-go` in-process) → sha256 verify →
atomic `rename(2)`. Refuse swap on any verification failure. Daily check
against the GitHub releases API; if a newer version exists, print after
the command output finishes: "v1.2.3 is available; run `apigw upgrade`."
Same UX `gh` uses; never blocks, never auto-updates.

## 9. Supply chain

- `govulncheck ./...` in CI on every PR (`golang.org/x/vuln/cmd/govulncheck`).
- Dependabot weekly, grouped PRs.
- Pin Go in `go.mod` + `go-version-file` in workflows; no `tip` builds.
- `GOFLAGS=-mod=readonly` in CI; reject `replace` directives outside of
  internal forks.
- Minimal dep set — every dep is an attack surface. Audit each at addition.

## 10. Audit logging

`log/slog` JSON handler → stdout → journald (`StandardOutput=journal+console`
in the unit). Fields: `actor_uid`, `actor_user`, `action`, `target`,
`result`, `error_class`, `request_id`. Redact via an **allowlist** in
`slog.HandlerOptions.ReplaceAttr` — never a denylist (denylists miss
fields added later). `journalctl -u apigw -o json` is the audit log.

## 11. Threat model summary

| Threat | Mitigation |
| --- | --- |
| MITM on `curl \| sudo bash` | TLS 1.2+ pinned in script; cosign-verified checksums; pinned `APIGW_VERSION` |
| Compromised release artifact | SLSA L3 provenance + keyless cosign signatures bound to our workflow OIDC identity |
| Stolen webhook secret | Per-app secrets `0600`; rotation via `apigw webhook rotate-secret`; constant-time HMAC; delivery-ID replay window |
| Timing attack on HMAC | `hmac.Equal` only |
| Local priv-esc via dashboard | Dedicated user, `NoNewPrivileges`, full systemd sandbox, only `CAP_NET_BIND_SERVICE` |
| Secret exfil from disk image / backup | `systemd-creds` (TPM-bound) opt-in; sops+age for git-tracked configs |
| Malicious dependency | `govulncheck` CI gate, `go.sum` enforcement, minimal deps, Dependabot review |
| Self-update RCE vector | Cosign signature + SHA256 verified **before** `rename(2)`; refuse swap on any failure |

## 12. Reference URLs

- GoReleaser supply chain: <https://goreleaser.com/blog/supply-chain-security/>
- Example: <https://github.com/goreleaser/example-supply-chain>
- SLSA 3 GitHub Actions builder: <https://github.com/slsa-framework/slsa-github-generator/blob/main/.github/workflows/builder_go_slsa3.yml>
- GitHub blog on SLSA 3 with Actions: <https://github.blog/security/supply-chain-security/slsa-3-compliance-with-github-actions/>
- Cosign sign-blob docs: <https://docs.sigstore.dev/cosign/signing/signing_with_blobs/>
- GitHub webhook verification: <https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries>
- systemd CREDENTIALS: <https://systemd.io/CREDENTIALS/>
- smallstep on systemd-creds: <https://smallstep.com/blog/systemd-creds-hardware-protected-secrets/>
- Caddy upstream unit (as a counter-example): <https://github.com/caddyserver/dist/blob/master/init/caddy.service>
- Tailscale install script: <https://tailscale.com/install.sh>
- Mozilla sops: <https://github.com/mozilla/sops>
- govulncheck: <https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck>

---

# Cross-cutting decisions (the one-page summary)

| Concern | Decision | Rationale (§) |
| --- | --- | --- |
| Language / runtime | Go 1.23+, `CGO_ENABLED=0`, static binary | Go architecture |
| CLI framework | Cobra + Factory DI | Go architecture |
| Config | knadh/koanf v2 (not viper) | Go architecture |
| Logging | `log/slog` + `charmbracelet/log` handler | Go architecture |
| FS abstraction | `spf13/afero` (testability) | Go architecture |
| Interactive prompts | `charmbracelet/huh` Groups | CLI design |
| Styling | `charmbracelet/lipgloss` AdaptiveColor | CLI design |
| Spinners / progress | `charmbracelet/bubbles` (one global) | CLI design |
| ACME client | `go-acme/lego/v4` as library (not certbot, not CertMagic) | TLS |
| Renewal | systemd timer calling `apigw tls renew` | TLS |
| Cert storage | `/var/lib/apigw/{acme,certs}/` 0600 | TLS |
| OCSP stapling | **OFF** for LE certs (LE shut OCSP down 2025-08-06) | TLS |
| Process supervision | systemd template units (`apigw-deploy@.service`) | Deploy |
| Build sandboxing | `systemd-run --scope` + dedicated `apigw-build` user | Deploy |
| Runtime detection | Deterministic decision tree (Dockerfile > go.mod > package.json > lockfiles > static) | Deploy |
| Zero-downtime | nginx upstream switch + socket activation | Deploy |
| Git deploy | HMAC-verified webhook → BoltDB queue → single-flight worker | Deploy |
| Event hub | Hand-rolled (~100 LoC), monotonic IDs, ring buffer 1024/topic | Live streaming |
| Streaming transport | SSE over HTTP/2 (not WebSocket, not gRPC-web) | Live streaming |
| Backpressure | Drop-oldest from subscriber queue; disconnect after N drops | Live streaming |
| Log tailing | `nxadm/tail` + `coreos/go-systemd/v22/sdjournal` | Live streaming |
| Dashboard front-end | Vanilla JS + `EventSource` + rAF batching | Live streaming |
| Release tool | goreleaser v2 with cosign + SBOM + nfpm | Security |
| Provenance | SLSA L3 via `slsa-github-generator` | Security |
| Signing | Keyless cosign (Sigstore + GH OIDC) | Security |
| Install script | <80 lines, cosign-verifies checksums before `install -m 0755` | Security |
| Webhook HMAC | `hmac.Equal` constant-time; UUID replay LRU | Security |
| Default secrets | filesystem 0600; `systemd-creds` opt-in (TPM2) | Security |
| Self-update | Explicit `apigw upgrade`, verify-before-`rename(2)` | Security |
| Privilege model | Root via sudo for mutators; `CAP_NET_BIND_SERVICE` for daemon | Security |
| Testing | unit + table-driven cobra + golden (autogold) + Docker e2e | Go architecture |
| CI | GitHub Actions; govulncheck; goreleaser dry-run on every PR | Security |

This table is the single source of truth for "what did we decide?". Any
change to a row needs a paragraph of new rationale added to the relevant
brief above. Don't edit the table alone.
