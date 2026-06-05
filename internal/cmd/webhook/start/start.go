// Package start implements `apigw webhook start`.
package start

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdStart(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Install the systemd unit and start the webhook server",
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
	if cfg.Webhook.Port == 0 {
		cfg.Webhook.Port = 9000
	}
	if cfg.Webhook.Path == "" {
		cfg.Webhook.Path = "/webhook"
	}
	addr := fmt.Sprintf(":%d", cfg.Webhook.Port)

	plan := tui.NewPlan("Start webhook receiver").
		Add("Listen", addr).
		Add("nginx mount", cfg.Webhook.Path).
		Add("Systemd unit", "/etc/systemd/system/apigw-webhook.service (install + enable)")
	plan.Render(f.IOStreams.Out, f.IOStreams)

	cfg.Webhook.Enabled = true
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := system.InstallWebhookUnit(system.ResolveSelfBinary(), addr); err != nil {
		return tui.NewError("install webhook unit failed", err.Error()).
			WithDocs("E_WEBHOOK_UNIT")
	}

	// Re-render nginx so /webhook/ is exposed.
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}

	ios := f.IOStreams
	fmt.Fprintf(ios.Out, "%s webhook server running on %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck), addr)
	fmt.Fprintf(ios.Out, "%s nginx proxies %s → 127.0.0.1%s\n",
		tui.Styles.Muted.Render("›"), cfg.Webhook.Path, addr)
	fmt.Fprintf(ios.Out, "  %s apigw webhook setup <deploy>\n",
		tui.Styles.Accent.Render("Next:"))
	return nil
}
