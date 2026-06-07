// Package slo owns `apigw api slo …` — set / show / clear the
// service-level objective on one API. The evaluator
// (internal/sla.Evaluate) runs against live metrics and reports
// breaches via the alerts pipeline + audit log.
package slo

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdSLO(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slo <command>",
		Short: "Per-API service-level objectives (latency p95, availability)",
		Example: `  $ apigw api slo set billing --latency-p95 200 --availability 99.9 --period 24
  $ apigw api slo show billing
  $ apigw api slo clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var latency, period int
	var availability float64
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set or update the SLO on an API",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if latency == 0 && availability == 0 {
				return fmt.Errorf("at least one of --latency-p95 / --availability must be non-zero")
			}
			if latency < 0 {
				return fmt.Errorf("--latency-p95 must be >= 0")
			}
			if availability < 0 || availability > 100 {
				return fmt.Errorf("--availability must be in 0..100")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.SLO = &config.SLO{
				LatencyP95Ms:        latency,
				AvailabilityPercent: availability,
				PeriodHours:         period,
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "slo set on %s: latency_p95=%dms availability=%.2f%% period=%dh\n",
				args[0], latency, availability, period)
			return nil
		},
	}
	cmd.Flags().IntVar(&latency, "latency-p95", 0, "alert if request p95 latency > this many ms (0 = disabled)")
	cmd.Flags().Float64Var(&availability, "availability", 0, "alert if (success/total) falls below this percent (0 = disabled)")
	cmd.Flags().IntVar(&period, "period", 0, "rolling window for availability (hours; 0 = default 1h)")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the SLO configured on an API",
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
			if api.SLO == nil {
				fmt.Fprintf(out, "%s: no SLO\n", args[0])
				return nil
			}
			s := api.SLO
			fmt.Fprintf(out, "%s SLO:\n", args[0])
			fmt.Fprintf(out, "  latency p95:  %s\n", dashIfZeroI(s.LatencyP95Ms, "ms"))
			fmt.Fprintf(out, "  availability: %s\n", dashIfZeroF(s.AvailabilityPercent, "%"))
			fmt.Fprintf(out, "  period:       %s\n", dashIfZeroI(s.PeriodHours, "h"))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the SLO",
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
			if api.SLO == nil {
				fmt.Fprintln(f.IOStreams.Out, "no SLO to clear")
				return nil
			}
			api.SLO = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "slo cleared on %s\n", args[0])
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

func dashIfZeroI(n int, unit string) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%s", n, unit)
}

func dashIfZeroF(n float64, unit string) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f%s", n, unit)
}
