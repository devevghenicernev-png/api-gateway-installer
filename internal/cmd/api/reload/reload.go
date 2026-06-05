// Package reload implements `apigw api reload` — re-render the nginx config
// from the current /etc/apigw/config.yaml and reload nginx.
//
// Useful after manual edits to config.yaml or when adding multiple APIs with
// --no-reload (batch mode).
package reload

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdReload(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Regenerate nginx config and reload",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			dryRun, _ := c.Flags().GetBool("dry-run")
			return run(f, dryRun)
		},
	}
}

func run(f *cmdutil.Factory, dryRun bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	mgr := nginx.NewManager()
	if dryRun {
		serverBody, httpBody, err := mgr.Render(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(f.IOStreams.Out, "# --- %s ---\n", nginx.SitePath)
		fmt.Fprintln(f.IOStreams.Out, string(serverBody))
		fmt.Fprintf(f.IOStreams.Out, "\n# --- %s ---\n", nginx.HTTPConfPath)
		fmt.Fprintln(f.IOStreams.Out, string(httpBody))
		return nil
	}
	if err := mgr.WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("apigw doctor", "diagnose nginx state").
			WithDocs("E_NGINX_RELOAD")
	}
	if !f.IOStreams.Quiet() {
		fmt.Fprintf(f.IOStreams.Out, "%s nginx reloaded with %d api(s)\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			len(cfg.APIs),
		)
	}
	return nil
}
