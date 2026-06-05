// Package stop implements `apigw dashboard stop`.
package stop

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdStop(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop and uninstall the dashboard service",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(f)
		},
	}
}

func run(f *cmdutil.Factory) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	cfg.Dashboard.Enabled = false
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := system.UninstallDashboardUnit(); err != nil {
		fmt.Fprintf(f.IOStreams.ErrOut, "%s could not uninstall unit: %s\n",
			tui.Styles.Warn.Render("!"), err.Error())
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(f.IOStreams.Out, "%s dashboard stopped\n",
		tui.Styles.Success.Render(tui.GlyphCheck))
	return nil
}
