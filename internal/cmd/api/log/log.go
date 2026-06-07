// Package log owns `apigw api log …` — per-route access log
// format and destination file overrides. Default is to inherit the
// global Logging.Format.
package log

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

var validModes = []string{"", "json", "combined", "off"}

func NewCmdLog(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log <command>",
		Short: "Per-route access log format and file",
		Example: `  $ apigw api log set billing --format json --file /var/log/billing.log
  $ apigw api log set internal --format off
  $ apigw api log show billing
  $ apigw api log clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var format, file string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set access log format / file for one API",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ok := false
			for _, m := range validModes {
				if format == m {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("--format must be one of: json, combined, off, or empty")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.AccessLog = format
			api.AccessLogFile = file
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "log set on %s: format=%s file=%s\n",
				args[0], dashIfEmpty(format), dashIfEmpty(file))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "log format: json / combined / off (empty = inherit global)")
	cmd.Flags().StringVar(&file, "file", "", "dedicated access log file path (empty = nginx default)")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the per-route log config",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			out := f.IOStreams.Out
			if api.AccessLog == "" && api.AccessLogFile == "" {
				fmt.Fprintf(out, "%s: no override (inherits global Logging.Format)\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s log:\n", args[0])
			fmt.Fprintf(out, "  format: %s\n", dashIfEmpty(api.AccessLog))
			fmt.Fprintf(out, "  file:   %s\n", dashIfEmpty(api.AccessLogFile))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove per-route log overrides",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if api.AccessLog == "" && api.AccessLogFile == "" {
				fmt.Fprintln(f.IOStreams.Out, "no overrides to clear")
				return nil
			}
			api.AccessLog = ""
			api.AccessLogFile = ""
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "log overrides cleared on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
		},
	}
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

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
