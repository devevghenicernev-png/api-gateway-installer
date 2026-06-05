// Package install implements `apigw install` — the first-run wizard.
//
// Interactive when stdin/stdout are TTYs; unattended when --config is passed.
// --plan / --dry-run renders the plan card and exits without touching disk.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
)

// Answers is the full set of wizard inputs. Saved by --print-config, loaded
// back by --config. The on-disk format is YAML so a human can review/edit it.
type Answers struct {
	HTTPPort         int    `yaml:"http_port" koanf:"http_port"`
	ServerName       string `yaml:"server_name" koanf:"server_name"`
	DashboardEnabled bool   `yaml:"dashboard_enabled" koanf:"dashboard_enabled"`
	DashboardPort    int    `yaml:"dashboard_port" koanf:"dashboard_port"`
	WebhookEnabled   bool   `yaml:"webhook_enabled" koanf:"webhook_enabled"`
	WebhookPort      int    `yaml:"webhook_port" koanf:"webhook_port"`
	ConfigPath       string `yaml:"config_path" koanf:"config_path"`

	// TLS (set when user picks a strategy; "skip"/"" means HTTP-only).
	TLSStrategy   string `yaml:"tls_strategy" koanf:"tls_strategy"`
	TLSDomain     string `yaml:"tls_domain" koanf:"tls_domain"`
	TLSEmail      string `yaml:"tls_email" koanf:"tls_email"`
	TLSSubdomain  string `yaml:"tls_subdomain" koanf:"tls_subdomain"`
	TLSToken      string `yaml:"tls_token" koanf:"tls_token"`
	TLSCommonName string `yaml:"tls_cn" koanf:"tls_cn"`
	TLSStaging    bool   `yaml:"tls_staging" koanf:"tls_staging"`
}

func defaults() Answers {
	return Answers{
		HTTPPort:         80,
		ServerName:       "_",
		DashboardEnabled: true,
		DashboardPort:    9080,
		WebhookEnabled:   false,
		WebhookPort:      9000,
		ConfigPath:       "/etc/apigw/config.yaml",
		TLSStrategy:      "skip",
	}
}

type options struct {
	f *cmdutil.Factory

	configFile  string
	printConfig bool
	yes         bool
	dryRun      bool
	plan        bool
}

func NewCmdInstall(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up apigw on this host (interactive wizard)",
		Args:  cobra.NoArgs,
		Example: `  $ apigw install                        # interactive
  $ apigw install --plan                 # show what would happen, exit
  $ apigw install --config install.yml   # unattended
  $ apigw install --print-config > install.yml`,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.configFile, "config-file", "",
		"YAML file with pre-answered wizard inputs (unattended install)")
	cmd.Flags().BoolVar(&opts.printConfig, "print-config", false,
		"print the answer YAML to stdout and exit (use with > install.yml)")
	cmd.Flags().BoolVar(&opts.plan, "plan", false,
		"render the plan card and exit (same as --dry-run)")
	return cmd
}

func run(opts *options) error {
	a, err := gatherAnswers(opts)
	if err != nil {
		return err
	}

	if opts.printConfig {
		return printAnswers(opts, a)
	}

	planCard := buildPlan(a)
	planCard.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun || opts.plan {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}

	if !opts.yes && opts.f.IOStreams.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Apply this plan?", "", true)
		if err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return err
		}
		if !ok {
			fmt.Fprintln(opts.f.IOStreams.ErrOut, tui.Styles.Muted.Render("aborted."))
			return cmdutil.SilentError
		}
	}

	return apply(opts, a)
}

// gatherAnswers resolves the wizard answers from --config-file or by prompting.
// Returns defaults() when running in unattended mode with no overrides.
func gatherAnswers(opts *options) (Answers, error) {
	a := defaults()
	if opts.configFile != "" {
		k := koanf.New(".")
		if err := k.Load(file.Provider(opts.configFile), yaml.Parser()); err != nil {
			return a, fmt.Errorf("read %s: %w", opts.configFile, err)
		}
		if err := k.Unmarshal("", &a); err != nil {
			return a, fmt.Errorf("decode %s: %w", opts.configFile, err)
		}
		return a, nil
	}
	if opts.printConfig {
		return a, nil
	}
	if !opts.f.IOStreams.IsStdinTTY() {
		// No TTY, no --config-file → run with defaults. CI-friendly.
		return a, nil
	}
	return runWizard(opts.f, a)
}

func printAnswers(opts *options, a Answers) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(opts.f.IOStreams.Out, string(b))
	return nil
}

func buildPlan(a Answers) *tui.PlanCard {
	dashboard := "disabled"
	if a.DashboardEnabled {
		dashboard = fmt.Sprintf("enabled on :%d", a.DashboardPort)
	}
	webhook := "disabled"
	if a.WebhookEnabled {
		webhook = fmt.Sprintf("enabled on :%d", a.WebhookPort)
	}
	tlsLabel := tlsPlanLabel(a)
	return tui.NewPlan("Install apigw").
		Add("HTTP port", fmt.Sprintf("%d", a.HTTPPort)).
		Add("Server name", a.ServerName).
		Add("TLS", tlsLabel).
		Add("Dashboard", dashboard).
		Add("Webhook", webhook).
		Add("Config", a.ConfigPath).
		Add("Nginx site", nginx.SitePath)
}

func tlsPlanLabel(a Answers) string {
	switch a.TLSStrategy {
	case "letsencrypt":
		s := "Let's Encrypt for " + a.TLSDomain
		if a.TLSStaging {
			s += " (staging)"
		}
		return s
	case "duckdns":
		return "DuckDNS — " + a.TLSSubdomain + ".duckdns.org"
	case "self-signed":
		return "self-signed (" + a.TLSCommonName + ")"
	default:
		return "deferred — run `apigw tls enable` later"
	}
}

func apply(opts *options, a Answers) error {
	if err := preflight(opts); err != nil {
		return err
	}

	// Idempotent install: if a config exists at the target path, load it and
	// preserve APIs/Deploys. Only listen/dashboard/webhook are wizard-driven.
	var cfg config.Config
	if _, err := os.Stat(a.ConfigPath); err == nil {
		existing, lerr := config.LoadFrom(a.ConfigPath)
		if lerr != nil {
			return fmt.Errorf("load existing config: %w", lerr)
		}
		cfg = *(existing.(*config.Config))
	} else {
		cfg = config.Defaults()
	}
	cfg.Listen.HTTPPort = a.HTTPPort
	cfg.Listen.ServerName = a.ServerName
	cfg.Dashboard.Enabled = a.DashboardEnabled
	cfg.Dashboard.Port = a.DashboardPort
	if cfg.Dashboard.Path == "" {
		cfg.Dashboard.Path = "/dashboard"
	}
	cfg.Webhook.Enabled = a.WebhookEnabled
	cfg.Webhook.Port = a.WebhookPort
	if cfg.Webhook.Path == "" {
		cfg.Webhook.Path = "/webhook"
	}
	cfg.SetPath(a.ConfigPath)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s wrote %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(a.ConfigPath))

	mgr := nginx.NewManager()
	if err := mgr.WriteAndReload(&cfg); err != nil {
		return tui.NewError("nginx setup failed", err.Error()).
			WithFix("apigw doctor", "diagnose nginx state").
			WithFix("sudo nginx -t", "see the underlying nginx error").
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s wrote %s and reloaded nginx\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(nginx.SitePath))

	// Optional TLS step.
	if err := applyTLS(opts, &cfg, a); err != nil {
		return err
	}

	fmt.Fprintln(opts.f.IOStreams.Out)
	fmt.Fprintf(opts.f.IOStreams.Out, "%s apigw is ready\n", tui.Styles.Success.Render(tui.GlyphCheck))
	fmt.Fprintf(opts.f.IOStreams.Out, "  %s apigw api add <name> --port <port>\n",
		tui.Styles.Accent.Render("Next:"))
	if a.TLSStrategy == "" || a.TLSStrategy == "skip" {
		fmt.Fprintf(opts.f.IOStreams.Out, "  %s apigw tls enable letsencrypt --domain <fqdn> --email <addr>\n",
			tui.Styles.Accent.Render("Then:"))
	}
	return nil
}

// applyTLS runs the chosen TLS strategy after nginx is up on :80. The HTTP-01
// challenge uses the webroot location we baked into the HTTP server template,
// so nginx doesn't have to come down.
func applyTLS(opts *options, cfg *config.Config, a Answers) error {
	if a.TLSStrategy == "" || a.TLSStrategy == "skip" {
		return nil
	}
	strategy := apitls.Strategy(a.TLSStrategy)
	if !strategy.IsValid() || strategy == apitls.StrategyNone {
		return fmt.Errorf("install: unknown tls strategy %q", a.TLSStrategy)
	}

	req := apitls.ObtainRequest{
		Strategy: strategy,
		Email:    a.TLSEmail,
		Staging:  a.TLSStaging,
	}
	switch strategy {
	case apitls.StrategyLetsEncrypt:
		req.Domains = []string{a.TLSDomain}
	case apitls.StrategyDuckDNS:
		full := a.TLSSubdomain
		if !endsWith(full, ".duckdns.org") {
			full += ".duckdns.org"
		}
		req.Domains = []string{full}
		req.DuckDNSToken = a.TLSToken
	case apitls.StrategySelfSigned:
		req.CommonName = a.TLSCommonName
		req.Domains = []string{a.TLSCommonName}
	}

	fmt.Fprintf(opts.f.IOStreams.Out, "\n%s requesting certificate (%s)\n",
		tui.Styles.Muted.Render("›"), strategy)
	if err := apitls.Obtain(req, false); err != nil {
		return tui.NewError("certificate issuance failed", err.Error()).
			WithFix("apigw tls enable "+string(strategy)+" --staging", "test against the LE staging CA first").
			WithFix("apigw doctor", "diagnose network/dns/firewall issues").
			WithDocs("E_TLS_OBTAIN")
	}

	cfg.TLS = config.TLS{
		Strategy:     string(strategy),
		Domains:      req.Domains,
		Email:        req.Email,
		Staging:      req.Staging,
		DuckDNSToken: req.DuckDNSToken,
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save tls config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx TLS reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}
	if strategy != apitls.StrategySelfSigned {
		if err := system.InstallTLSRenewTimer(system.ResolveSelfBinary()); err != nil {
			fmt.Fprintf(opts.f.IOStreams.ErrOut, "%s could not install renewal timer: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(opts.f.IOStreams.Out, "%s renewal timer enabled\n",
				tui.Styles.Success.Render(tui.GlyphCheck))
		}
	}
	for _, d := range req.Domains {
		fmt.Fprintf(opts.f.IOStreams.Out, "%s https://%s\n",
			tui.Styles.Success.Render(tui.GlyphArrow),
			tui.Styles.Identifier.Render(d))
	}
	return nil
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// preflight checks the bare minimum: we're root (effective UID 0) and nginx
// is on PATH. Phase 1 doesn't auto-install nginx — that's Phase 8 platform work.
func preflight(opts *options) error {
	if euid := os.Geteuid(); euid != 0 {
		return cmdutil.NewPermissionError(
			fmt.Errorf("install must run as root (effective uid %d)", euid),
			"re-run with sudo: `sudo apigw install`",
		)
	}
	if _, err := exec.LookPath("nginx"); err != nil {
		return tui.NewError(
			"nginx is not installed",
			"apigw routes traffic through nginx; install it first",
		).WithFix("sudo apt-get install -y nginx", "Debian/Ubuntu").
			WithFix("sudo dnf install -y nginx", "RHEL/Fedora").
			WithDocs("E_NGINX_MISSING")
	}
	return nil
}
