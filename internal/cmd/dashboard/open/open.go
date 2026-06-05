// Package open implements `apigw dashboard open` — open the dashboard URL
// in the user's default browser via xdg-open / open.
package open

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdOpen(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "open",
		Short: "Open the dashboard in the default browser",
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
	scheme := "http"
	host := "127.0.0.1"
	if cfg.TLS.Strategy != "" && cfg.TLS.Strategy != "none" {
		scheme = "https"
		if len(cfg.TLS.Domains) > 0 {
			host = cfg.TLS.Domains[0]
		}
	}
	port := cfg.Dashboard.Port
	if port == 0 {
		port = 9080
	}
	url := fmt.Sprintf("%s://%s:%d/", scheme, host, port)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(f.IOStreams.Out, "%s open %s\n",
			tui.Styles.Accent.Render(tui.GlyphArrow),
			tui.Styles.URL.Render(url))
		return nil // not a hard failure — we printed it
	}
	fmt.Fprintf(f.IOStreams.Out, "%s opened %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.URL.Render(url))
	return nil
}
