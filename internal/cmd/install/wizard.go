package install

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// runWizard collects answers via a 5-step huh Form. Conditional groups
// (per-strategy TLS inputs) use WithHideFunc so we don't pop irrelevant
// fields. Esc within a Group rewinds; Ctrl-C cancels cleanly.
func runWizard(f *cmdutil.Factory, def Answers) (Answers, error) {
	a := def
	httpPort := strconv.Itoa(a.HTTPPort)
	dashPort := strconv.Itoa(a.DashboardPort)
	whPort := strconv.Itoa(a.WebhookPort)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 1 of 5").
				Description("Listen settings. nginx is the entrypoint; pick the port and server name."),
			huh.NewInput().
				Title("HTTP port").
				Description("80 is the default. Use 8080 for unprivileged dev installs.").
				Value(&httpPort).
				Validate(portValidator),
			huh.NewInput().
				Title("Server name").
				Description("nginx server_name. Use _ to catch any host.").
				Value(&a.ServerName),
		),

		// ---- Step 2: TLS strategy ----
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 2 of 5").
				Description("HTTPS protects requests in transit and is required by browsers\n"+
					"and mobile app stores. Pick one — switch later with `apigw tls enable`."),
			huh.NewSelect[string]().
				Title("TLS strategy").
				Description("Pick one. Skip → HTTP only; enable later anytime.").
				Options(
					huh.NewOption("Let's Encrypt (free, trusted, needs a public domain)", "letsencrypt"),
					huh.NewOption("DuckDNS (free *.duckdns.org — no domain needed)", "duckdns"),
					huh.NewOption("Self-signed (works locally only; clients must trust the cert)", "self-signed"),
					huh.NewOption("Skip TLS (HTTP only — recommended for dev/initial install)", "skip"),
				).
				Value(&a.TLSStrategy),
		),

		// ---- Step 2a: LE-specific ----
		huh.NewGroup(
			huh.NewInput().
				Title("Public domain").
				Description("FQDN pointing at this host (e.g. api.example.com).").
				Value(&a.TLSDomain).
				Validate(nonEmpty),
			huh.NewInput().
				Title("Email").
				Description("Used by Let's Encrypt for renewal warnings.").
				Value(&a.TLSEmail).
				Validate(emailValidator),
			huh.NewConfirm().
				Title("Use staging CA?").
				Description("Recommended for first attempt — production has weekly cert limits.").
				Value(&a.TLSStaging),
		).WithHideFunc(func() bool { return a.TLSStrategy != "letsencrypt" }),

		// ---- Step 2b: DuckDNS-specific ----
		huh.NewGroup(
			huh.NewInput().
				Title("DuckDNS subdomain").
				Description("The part before .duckdns.org (e.g. orangepiapi).").
				Value(&a.TLSSubdomain).
				Validate(nonEmpty),
			huh.NewInput().
				Title("DuckDNS API token").
				Description("Get yours at https://www.duckdns.org/ — looks like fe1591ff-…").
				Value(&a.TLSToken).
				Validate(nonEmpty),
			huh.NewInput().
				Title("Email").
				Description("Used by Let's Encrypt for renewal warnings.").
				Value(&a.TLSEmail).
				Validate(emailValidator),
		).WithHideFunc(func() bool { return a.TLSStrategy != "duckdns" }),

		// ---- Step 2c: self-signed-specific ----
		huh.NewGroup(
			huh.NewInput().
				Title("Common Name").
				Description("Hostname for the self-signed cert (e.g. orangepi.local).").
				Value(&a.TLSCommonName).
				Validate(nonEmpty),
		).WithHideFunc(func() bool { return a.TLSStrategy != "self-signed" }),

		// ---- Step 3: dashboard ----
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 3 of 5").
				Description("Dashboard. A live UI on /dashboard (Server-Sent Events, no polling)."),
			huh.NewConfirm().
				Title("Enable the dashboard?").
				Value(&a.DashboardEnabled),
			huh.NewInput().
				Title("Dashboard port").
				Description("Local port; nginx proxies /dashboard here.").
				Value(&dashPort).
				Validate(portValidator),
		),

		// ---- Step 4: webhook ----
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 4 of 5").
				Description("Webhook receiver. Enable later if you don't have a git deploy yet."),
			huh.NewConfirm().
				Title("Enable webhooks?").
				Value(&a.WebhookEnabled),
			huh.NewInput().
				Title("Webhook port").
				Description("Local port; nginx proxies /webhook here.").
				Value(&whPort).
				Validate(portValidator),
		),

		// ---- Step 5: admin API security ----
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 5 of 6").
				Description("Admin API security. With this on, /api/admin/* (the\n"+
					"mutation endpoints behind the dashboard) require a bearer\n"+
					"token. We'll bootstrap one owner-token for you and start\n"+
					"in SOFT-ROLLOUT mode (denials are audit-logged, not enforced).\n"+
					"You promote to hard-enforce later with `apigw auth admin enforce`."),
			huh.NewConfirm().
				Title("Enable RBAC on the admin API?").
				Description("Strongly recommended for anything shared with teammates.").
				Value(&a.SecurityEnabled),
			huh.NewInput().
				Title("Owner identity").
				Description("Subject name for the bootstrap token (e.g. 'admin', your username).").
				Value(&a.SecurityUser).
				Validate(nonEmpty),
		),

		// ---- Step 6: storage ----
		huh.NewGroup(
			huh.NewNote().
				Title("apigw install — step 6 of 6").
				Description("Storage. /etc/apigw/config.yaml is the conventional location."),
			huh.NewInput().
				Title("Config file path").
				Value(&a.ConfigPath),
		),
	).WithTheme(huh.ThemeCharm())

	if err := form.Run(); err != nil {
		return a, err
	}

	if n, err := strconv.Atoi(httpPort); err == nil {
		a.HTTPPort = n
	}
	if n, err := strconv.Atoi(dashPort); err == nil {
		a.DashboardPort = n
	}
	if n, err := strconv.Atoi(whPort); err == nil {
		a.WebhookPort = n
	}
	return a, nil
}

func portValidator(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a number")
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("must be between 1 and 65535")
	}
	return nil
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func emailValidator(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	at := strings.IndexByte(s, '@')
	if at < 1 || at == len(s)-1 || strings.IndexByte(s[at+1:], '.') < 0 {
		return fmt.Errorf("looks invalid")
	}
	return nil
}
