// Package renew implements `apigw tls renew`.
//
// Invoked both interactively and from the systemd timer. Same code path —
// only difference is TTY-adaptive output.
package renew

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f      *cmdutil.Factory
	force  bool
	dryRun bool
	asJSON bool
}

func NewCmdRenew(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "renew",
		Short: "Renew certificates near expiry",
		Long: "Renews any certificate within 30 days of expiry. Run by the\n" +
			"systemd timer; safe to invoke manually anytime.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			opts.asJSON, _ = c.Flags().GetBool("json")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.force, "force", false, "renew even if not near expiry")
	return cmd
}

func run(opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}

	results, err := apitls.RenewAll(apitls.RenewOptions{
		Force:        opts.force,
		DryRun:       opts.dryRun,
		Email:        cfg.TLS.Email,
		Staging:      cfg.TLS.Staging,
		DuckDNSToken: cfg.TLS.DuckDNSToken,
	})
	if err != nil {
		return err
	}

	if opts.asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Fprintln(opts.f.IOStreams.Out, string(b))
		return nil
	}

	ios := opts.f.IOStreams
	renewed := 0
	failed := 0
	for _, r := range results {
		switch {
		case r.Error != nil:
			failed++
			fmt.Fprintf(ios.Out, "%s %s — %s\n",
				tui.Styles.Danger.Render(tui.GlyphCross),
				tui.Styles.Identifier.Render(r.Domain),
				r.Error.Error())
		case r.Renewed:
			renewed++
			fmt.Fprintf(ios.Out, "%s %s renewed (expires %s, %d days)\n",
				tui.Styles.Success.Render(tui.GlyphCheck),
				tui.Styles.Identifier.Render(r.Domain),
				r.NewExpiry.Format("2006-01-02"),
				r.DaysLeft)
		default:
			fmt.Fprintf(ios.Out, "%s %s ok (%d days left)\n",
				tui.Styles.Muted.Render("›"),
				tui.Styles.Identifier.Render(r.Domain),
				r.DaysLeft)
		}
	}

	if renewed > 0 && !opts.dryRun {
		// Reload nginx so the new certs take effect.
		if err := nginx.NewManager().Reload(); err != nil {
			return tui.NewError("nginx reload failed", err.Error()).
				WithDocs("E_NGINX_RELOAD")
		}
		fmt.Fprintf(ios.Out, "%s nginx reloaded\n", tui.Styles.Success.Render(tui.GlyphCheck))
	}

	if failed > 0 {
		return cmdutil.SilentError
	}
	return nil
}
