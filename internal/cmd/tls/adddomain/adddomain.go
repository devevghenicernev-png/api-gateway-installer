// Package adddomain implements `apigw tls add-domain` — extends the current
// TLS strategy's certificate with an additional SAN. Re-uses the existing
// strategy + email + token, just appends to the domain list.
package adddomain

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f *cmdutil.Factory

	Domain string
	Force  bool
	Yes    bool
	DryRun bool
}

func NewCmdAddDomain(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "add-domain <fqdn>",
		Short: "Extend the current certificate with an additional SAN",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw tls add-domain api2.example.com
  $ apigw tls add-domain *.preview.example.com    # requires duckdns/dns-01 strategy`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.Domain = args[0]
			opts.Yes, _ = c.Flags().GetBool("yes")
			opts.DryRun, _ = c.Flags().GetBool("dry-run")
			return run(c, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"re-issue even if the domain is already in the SAN list")
	return cmd
}

func run(cmd *cobra.Command, opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	if cfg.TLS.Strategy == "" || cfg.TLS.Strategy == string(apitls.StrategyNone) {
		return tui.NewError(
			"TLS not enabled",
			"add-domain extends an existing cert — enable TLS first",
		).WithFix("apigw tls enable letsencrypt --domain "+opts.Domain+" --email <addr>", "fresh cert").
			WithDocs("E_TLS_NOT_ENABLED")
	}
	for _, d := range cfg.TLS.Domains {
		if d == opts.Domain && !opts.Force {
			fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render(
				"domain already in SAN list — pass --force to re-issue"))
			return nil
		}
	}

	domains := append([]string{}, cfg.TLS.Domains...)
	domains = append(domains, opts.Domain)

	plan := tui.NewPlan("Add domain to certificate").
		Add("Strategy", cfg.TLS.Strategy).
		Add("Existing SANs", joinList(cfg.TLS.Domains)).
		Add("New domain", opts.Domain).
		Add("Email", cfg.TLS.Email)
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.DryRun {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}
	if !opts.Yes && opts.f.IOStreams.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Re-issue with the extended SAN list?", "", true)
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

	req := apitls.ObtainRequest{
		Strategy:     apitls.Strategy(cfg.TLS.Strategy),
		Domains:      domains,
		Email:        cfg.TLS.Email,
		Staging:      cfg.TLS.Staging,
		DuckDNSToken: cfg.TLS.DuckDNSToken,
	}
	if err := apitls.Obtain(req, true); err != nil {
		return tui.NewError("re-issue failed", err.Error()).
			WithFix("apigw tls renew --force", "retry").
			WithDocs("E_TLS_OBTAIN")
	}

	cfg.TLS.Domains = domains
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s certificate now covers %d domain(s)\n",
		tui.Styles.Success.Render(tui.GlyphCheck), len(domains))
	return nil
}

func joinList(ds []string) string {
	if len(ds) == 0 {
		return "—"
	}
	out := ds[0]
	for i := 1; i < len(ds); i++ {
		out += ", " + ds[i]
	}
	return out
}
