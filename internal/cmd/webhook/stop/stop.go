// Package stop implements `apigw webhook stop`.
package stop

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdStop(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop and uninstall the webhook server",
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
	cfg.Webhook.Enabled = false
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := system.UninstallWebhookUnit(); err != nil {
		fmt.Fprintf(f.IOStreams.ErrOut, "%s could not uninstall unit: %s\n",
			tui.Styles.Warn.Render("!"), err.Error())
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(f.IOStreams.Out, "%s webhook server stopped\n",
		tui.Styles.Success.Render(tui.GlyphCheck))
	return nil
}
