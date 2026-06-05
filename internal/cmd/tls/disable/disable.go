// Package disable implements `apigw tls disable` — revert to HTTP-only.
//
// Does NOT delete cert files by default (they're kept under /var/lib/apigw/
// in case the user re-enables TLS). Pass --purge to remove them.
package disable

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f      *cmdutil.Factory
	purge  bool
	yes    bool
	dryRun bool
}

func NewCmdDisable(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Revert to HTTP-only — TLS off",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.purge, "purge", false,
		"also delete /var/lib/apigw/certs/<domain>/ for each disabled domain")
	return cmd
}

func run(opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	if cfg.TLS.Strategy == "" || cfg.TLS.Strategy == string(apitls.StrategyNone) {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("TLS is already disabled — nothing to do."))
		return nil
	}

	plan := tui.NewPlan("Disable TLS").
		Add("Strategy", cfg.TLS.Strategy).
		Add("Domains", joinDomains(cfg.TLS.Domains)).
		Add("Renewal timer", "uninstall").
		Add("Cert files", purgeLabel(opts.purge))
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}

	// Per ARCHITECTURE.md §"CLI design system" rule 4: destructive command
	// requires TypeToConfirm with the primary domain name. --yes still
	// available for headless / scripted teardowns.
	if !opts.yes && opts.f.IOStreams.IsStdinTTY() {
		primary := ""
		if len(cfg.TLS.Domains) > 0 {
			primary = cfg.TLS.Domains[0]
		}
		if primary == "" {
			ok, err := opts.f.Prompter.Confirm("Disable TLS?", "", false)
			if err != nil {
				if errors.Is(err, cmdutil.CancelError) {
					return cmdutil.CancelError
				}
				return err
			}
			if !ok {
				return cmdutil.SilentError
			}
		} else if err := opts.f.Prompter.TypeToConfirm(
			fmt.Sprintf("Type %q to confirm disable", primary), primary,
		); err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return cmdutil.SilentError
		}
	}

	domains := append([]string{}, cfg.TLS.Domains...)
	cfg.TLS = config.TLS{Strategy: string(apitls.StrategyNone)}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	mgr := nginx.NewManager()
	if err := mgr.WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("sudo nginx -t", "see the underlying nginx error").
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "%s nginx reverted to HTTP-only\n", tui.Styles.Success.Render(tui.GlyphCheck))

	if err := system.UninstallTLSRenewTimer(); err == nil {
		fmt.Fprintf(opts.f.IOStreams.Out, "%s renewal timer removed\n", tui.Styles.Success.Render(tui.GlyphCheck))
	} else {
		fmt.Fprintf(opts.f.IOStreams.ErrOut, "%s could not remove timer: %s\n",
			tui.Styles.Warn.Render("!"), err.Error())
	}

	if opts.purge {
		for _, d := range domains {
			if err := apitls.RemoveCert(d); err != nil {
				fmt.Fprintf(opts.f.IOStreams.ErrOut, "%s could not purge %s: %s\n",
					tui.Styles.Warn.Render("!"), d, err.Error())
			} else {
				fmt.Fprintf(opts.f.IOStreams.Out, "%s purged %s\n",
					tui.Styles.Success.Render(tui.GlyphCheck), d)
			}
		}
	}
	return nil
}

func joinDomains(ds []string) string {
	if len(ds) == 0 {
		return "—"
	}
	out := ds[0]
	for i := 1; i < len(ds); i++ {
		out += ", " + ds[i]
	}
	return out
}

func purgeLabel(p bool) string {
	if p {
		return "delete (--purge)"
	}
	return "keep on disk for re-enable"
}
