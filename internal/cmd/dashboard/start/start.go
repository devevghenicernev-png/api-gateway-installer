// Package start implements `apigw dashboard start`.
package start

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdStart(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Install the systemd unit and start the live dashboard",
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
	if cfg.Dashboard.Port == 0 {
		cfg.Dashboard.Port = 9080
	}
	if cfg.Dashboard.Path == "" {
		cfg.Dashboard.Path = "/dashboard"
	}
	if cfg.Webhook.Port == 0 {
		cfg.Webhook.Port = 9000
	}

	addr := fmt.Sprintf(":%d", cfg.Dashboard.Port)
	whAddr := fmt.Sprintf(":%d", cfg.Webhook.Port)

	plan := tui.NewPlan("Start dashboard").
		Add("Dashboard listen", addr).
		Add("Webhook listen", whAddr).
		Add("nginx mount", cfg.Dashboard.Path).
		Add("Systemd unit", "/etc/systemd/system/apigw-dashboard.service (install + enable)")
	plan.Render(f.IOStreams.Out, f.IOStreams)

	cfg.Dashboard.Enabled = true
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := system.InstallDashboardUnit(system.ResolveSelfBinary(), addr, whAddr); err != nil {
		return tui.NewError("install dashboard unit failed", err.Error()).
			WithDocs("E_DASH_UNIT")
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}

	ios := f.IOStreams
	fmt.Fprintf(ios.Out, "%s dashboard running on %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck), addr)
	fmt.Fprintf(ios.Out, "%s webhook receiver on %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck), whAddr)
	fmt.Fprintf(ios.Out, "  %s apigw dashboard open\n",
		tui.Styles.Accent.Render("Next:"))
	return nil
}
