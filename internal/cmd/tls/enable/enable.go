// Package enable implements `apigw tls enable <strategy> [flags]`.
//
// Three strategies share one command surface so the user doesn't have to
// remember three different command names. The first positional arg picks
// the strategy; flags are validated per-strategy.
package enable

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f *cmdutil.Factory

	Strategy string

	// Let's Encrypt / DuckDNS
	Domain  string
	Domains []string
	Email   string
	Staging bool

	// DuckDNS
	Subdomain string
	Token     string

	// Self-signed
	CommonName string

	// Flags
	yes     bool
	dryRun  bool
	noTimer bool // skip systemd timer install — useful in containers
}

func NewCmdEnable(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "enable <strategy>",
		Short: "Enable HTTPS for apigw",
		Args:  cobra.ExactArgs(1),
		Example: `  apigw tls enable letsencrypt --domain api.example.com --email me@example.com
  apigw tls enable duckdns --subdomain orangepiapi --token <tok> --email me@example.com
  apigw tls enable self-signed --cn orangepi.local`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.Strategy = strings.ToLower(args[0])
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Domain, "domain", "", "public domain (letsencrypt)")
	cmd.Flags().StringSliceVar(&opts.Domains, "domains", nil, "additional SAN domains (comma-separated)")
	cmd.Flags().StringVar(&opts.Email, "email", "", "ACME account email (letsencrypt|duckdns)")
	cmd.Flags().StringVar(&opts.Subdomain, "subdomain", "", "duckdns subdomain, without .duckdns.org")
	cmd.Flags().StringVar(&opts.Token, "token", "", "duckdns API token")
	cmd.Flags().StringVar(&opts.CommonName, "cn", "", "common name (self-signed)")
	cmd.Flags().BoolVar(&opts.Staging, "staging", false, "use Let's Encrypt staging CA (tests/CI)")
	cmd.Flags().BoolVar(&opts.noTimer, "no-timer", false, "skip systemd timer install")
	return cmd
}

func run(opts *options) error {
	strategy := apitls.Strategy(opts.Strategy)
	if !strategy.IsValid() || strategy == apitls.StrategyNone {
		return tui.NewError(
			"unknown tls strategy",
			fmt.Sprintf("got %q, want one of: letsencrypt, duckdns, self-signed", opts.Strategy),
		).WithFix("apigw tls enable letsencrypt --domain ... --email ...", "Let's Encrypt over HTTP-01").
			WithFix("apigw tls enable duckdns --subdomain ... --token ... --email ...", "DuckDNS over DNS-01").
			WithFix("apigw tls enable self-signed --cn ...", "Self-signed (dev/local)").
			WithDocs("E_TLS_STRATEGY")
	}

	req, err := buildRequest(opts, strategy)
	if err != nil {
		return err
	}

	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}

	plan := tui.NewPlan("Enable TLS").
		Add("Strategy", string(strategy)).
		Add("Domains", strings.Join(req.Domains, ", ")).
		Add("Email", emptyDash(req.Email)).
		Add("Storage", apitls.CertDir()+"/<domain>/").
		Add("Auto-renew", autoRenewLabel(opts.noTimer))
	if strategy == apitls.StrategyLetsEncrypt && opts.Staging {
		plan.Add("CA", "staging (test-only certs, not browser-trusted)")
	}
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun {
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
			return cmdutil.SilentError
		}
	}

	// Obtain (or generate, for self-signed).
	if err := apitls.Obtain(req, false); err != nil {
		return tui.NewError("certificate issuance failed", err.Error()).
			WithFix("apigw tls enable "+opts.Strategy+" --staging", "test against the LE staging CA first").
			WithFix("apigw doctor", "diagnose network/dns/firewall issues").
			WithDocs("E_TLS_OBTAIN")
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s certificate installed\n", tui.Styles.Success.Render(tui.GlyphCheck))

	// Persist config.
	cfg.TLS = config.TLS{
		Strategy:     string(strategy),
		Domains:      req.Domains,
		Email:        req.Email,
		Staging:      opts.Staging,
		DuckDNSToken: req.DuckDNSToken,
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Re-render nginx with TLS server block, validate, reload.
	mgr := nginx.NewManager()
	if err := mgr.WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("sudo nginx -t", "see the underlying nginx error").
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s nginx reloaded\n", tui.Styles.Success.Render(tui.GlyphCheck))

	// Install renewal timer (skippable in dev/CI).
	if !opts.noTimer && strategy != apitls.StrategySelfSigned {
		if err := system.InstallTLSRenewTimer(system.ResolveSelfBinary()); err != nil {
			return tui.NewError("renewal timer install failed", err.Error()).
				WithFix("apigw tls enable ... --no-timer", "skip the timer for now").
				WithDocs("E_TLS_TIMER")
		}
		fmt.Fprintf(opts.f.IOStreams.Out, "%s renewal timer enabled (twice daily, randomised)\n", tui.Styles.Success.Render(tui.GlyphCheck))
	}

	fmt.Fprintln(opts.f.IOStreams.Out)
	for _, d := range req.Domains {
		fmt.Fprintf(opts.f.IOStreams.Out, "%s https://%s\n",
			tui.Styles.Success.Render(tui.GlyphArrow),
			tui.Styles.Identifier.Render(d))
	}
	return nil
}

// buildRequest validates strategy-specific flags and returns a ready ObtainRequest.
func buildRequest(opts *options, strategy apitls.Strategy) (apitls.ObtainRequest, error) {
	req := apitls.ObtainRequest{
		Strategy: strategy,
		Email:    opts.Email,
		Staging:  opts.Staging,
	}
	switch strategy {
	case apitls.StrategyLetsEncrypt:
		if opts.Domain == "" {
			return req, cmdutil.FlagErrorf("--domain is required for letsencrypt")
		}
		if opts.Email == "" {
			return req, cmdutil.FlagErrorf("--email is required for letsencrypt")
		}
		req.Domains = dedup(append([]string{opts.Domain}, opts.Domains...))

	case apitls.StrategyDuckDNS:
		if opts.Subdomain == "" {
			return req, cmdutil.FlagErrorf("--subdomain is required for duckdns")
		}
		if opts.Token == "" {
			return req, cmdutil.FlagErrorf("--token is required for duckdns")
		}
		if opts.Email == "" {
			return req, cmdutil.FlagErrorf("--email is required for duckdns")
		}
		full := opts.Subdomain
		if !strings.HasSuffix(full, ".duckdns.org") {
			full = full + ".duckdns.org"
		}
		req.Domains = dedup(append([]string{full}, opts.Domains...))
		req.DuckDNSToken = opts.Token

	case apitls.StrategySelfSigned:
		if opts.CommonName == "" {
			return req, cmdutil.FlagErrorf("--cn is required for self-signed")
		}
		req.CommonName = opts.CommonName
		req.Domains = dedup(append([]string{opts.CommonName}, opts.Domains...))
	}
	return req, nil
}

func dedup(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func autoRenewLabel(skipped bool) string {
	if skipped {
		return "disabled (--no-timer)"
	}
	return "systemd timer (twice daily, randomised)"
}
