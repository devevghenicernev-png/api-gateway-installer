// Package configcmd owns `apigw config …` — static config checks
// (`lint`) plus future export/import knobs.
package configcmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/lint"
)

func NewCmdConfig(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config <command>",
		Short: "Inspect and validate the loaded config",
	}
	cmd.AddCommand(newLint(f))
	return cmd
}

func newLint(f *cmdutil.Factory) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Static checks over the loaded config (port collisions, missing refs, ...)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			findings := lint.Lint(cfg)

			if asJSON {
				return json.NewEncoder(f.IOStreams.Out).Encode(findings)
			}
			out := f.IOStreams.Out
			if len(findings) == 0 {
				fmt.Fprintln(out, "ok — no findings")
				return nil
			}
			errs, warns := 0, 0
			for _, fnd := range findings {
				fmt.Fprintf(out, "%-7s  %-4s  %-24s  %s\n",
					fnd.Severity.String(), fnd.Code, fnd.Resource, fnd.Message)
				if fnd.Severity == lint.SeverityError {
					errs++
				} else {
					warns++
				}
			}
			fmt.Fprintf(out, "\n%d error(s), %d warning(s)\n", errs, warns)
			if lint.HasErrors(findings) {
				return fmt.Errorf("lint failed: %d error(s)", errs)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit findings as JSON")
	return cmd
}

func loadCfg(f *cmdutil.Factory) (*config.Config, error) {
	c, err := f.Config()
	if err != nil {
		return nil, err
	}
	cfg, ok := c.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("config: unexpected type %T", c)
	}
	return cfg, nil
}
