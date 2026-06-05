// Package list implements `apigw webhook list`.
package list

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List deploys with webhook secrets configured",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			return run(f, asJSON)
		},
	}
	return cmd
}

type record struct {
	Deploy    string `json:"deploy"`
	URL       string `json:"url"`
	HasSecret bool   `json:"has_secret"`
}

func run(f *cmdutil.Factory, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	host := publicHost(cfg)
	https := cfg.TLS.Strategy != "" && cfg.TLS.Strategy != "none"

	rows := make([]record, 0, len(cfg.Deploys))
	for _, d := range cfg.Deploys {
		_, err := webhook.LoadSecret(d.Name)
		rows = append(rows, record{
			Deploy:    d.Name,
			URL:       webhook.PublicURL(host, d.Name, https),
			HasSecret: err == nil,
		})
	}

	if asJSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}

	ios := f.IOStreams
	if len(rows) == 0 {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("No deployments registered."))
		return nil
	}
	width := 0
	for _, r := range rows {
		if n := len(r.Deploy); n > width {
			width = n
		}
	}
	for _, r := range rows {
		mark := tui.Styles.Success.Render(tui.GlyphCheck)
		if !r.HasSecret {
			mark = tui.Styles.Warn.Render("!")
		}
		fmt.Fprintf(ios.Out, "%s  %s  %s\n",
			mark,
			tui.Styles.Identifier.Render(padRight(r.Deploy, width)),
			tui.Styles.URL.Render(r.URL))
	}
	fmt.Fprintln(ios.Out)
	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Use `apigw webhook setup <deploy>` to generate a secret and see GitHub steps."))
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

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
