// Package url implements `apigw dashboard url` — print the public URL.
package url

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/spf13/cobra"
)

func NewCmdURL(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "url",
		Short: "Print the public dashboard URL",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			fmt.Fprintln(f.IOStreams.Out, publicURL(cfg))
			return nil
		},
	}
}

func publicURL(cfg *config.Config) string {
	scheme := "http"
	if cfg.TLS.Strategy != "" && cfg.TLS.Strategy != "none" {
		scheme = "https"
	}
	host := "<your-host>"
	if len(cfg.TLS.Domains) > 0 {
		host = cfg.TLS.Domains[0]
	} else if cfg.Listen.ServerName != "" && cfg.Listen.ServerName != "_" {
		host = cfg.Listen.ServerName
	}
	path := cfg.Dashboard.Path
	if path == "" {
		path = "/dashboard"
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}
