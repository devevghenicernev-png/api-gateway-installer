// Package grafana owns `apigw dashboard grafana` — prints (or saves)
// the embedded Grafana dashboard JSON so operators can import it
// into their existing Grafana without re-creating the panels.
package grafana

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/assets"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// NewCmdGrafana returns `apigw dashboard grafana`.
func NewCmdGrafana(f *cmdutil.Factory) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "grafana",
		Short: "Print the embedded Grafana dashboard JSON (default apigw panels)",
		Long: "Prints the apigw.json Grafana dashboard to stdout, or writes it to --out.\n" +
			"Import the result via the Grafana UI (Dashboards → New → Import). The\n" +
			"datasource template variable defaults to a Prometheus datasource\n" +
			"named 'Prometheus' — change in Grafana UI after import if yours is\n" +
			"named differently.",
		Example: `  $ apigw dashboard grafana > apigw.json
  $ apigw dashboard grafana --out /etc/grafana/provisioning/dashboards/apigw.json`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			data, err := assets.Grafana().ReadFile("grafana/apigw.json")
			if err != nil {
				return fmt.Errorf("read embedded dashboard: %w", err)
			}
			var w io.Writer = f.IOStreams.Out
			if out != "" {
				file, err := os.Create(out)
				if err != nil {
					return fmt.Errorf("create %s: %w", out, err)
				}
				defer file.Close()
				w = file
			}
			if _, err := w.Write(data); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			if out != "" {
				fmt.Fprintf(f.IOStreams.Out, "wrote %d bytes to %s\n", len(data), out)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write to this file instead of stdout")
	return cmd
}
