// Package url implements `apigw webhook url <deploy>` — print the public URL.
package url

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
	"github.com/spf13/cobra"
)

func NewCmdURL(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "url <deploy>",
		Short: "Print the public webhook URL for a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(f, args[0])
		},
	}
}

func run(f *cmdutil.Factory, name string) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if cfg.FindDeploy(name) == nil {
		return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
			WithDocs("E_DEPLOY_NOT_FOUND")
	}
	host := publicHost(cfg)
	https := cfg.TLS.Strategy != "" && cfg.TLS.Strategy != "none"
	fmt.Fprintln(f.IOStreams.Out, webhook.PublicURL(host, name, https))
	return nil
}

func publicHost(cfg *config.Config) string {
	if len(cfg.TLS.Domains) > 0 {
		return cfg.TLS.Domains[0]
	}
	if cfg.Listen.ServerName != "" && cfg.Listen.ServerName != "_" {
		return cfg.Listen.ServerName
	}
	return "<your-host>"
}
