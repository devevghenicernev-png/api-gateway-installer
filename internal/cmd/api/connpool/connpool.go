// Package connpool owns `apigw api conn-pool …` — per-API upstream
// keepalive tuning. Maps onto nginx's upstream-block `keepalive` /
// `keepalive_timeout` / `keepalive_requests` directives.
//
// Defaults are fine for most workloads. Bump --conns above 16 when
// you have hot-path APIs with many upstreams, or --requests above
// 1000 to keep long-lived connections (gRPC streams, websockets)
// from rotating mid-flight.
package connpool

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdConnPool(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "conn-pool <command>",
		Aliases: []string{"connpool", "pool"},
		Short:   "Per-API upstream keepalive pool tuning",
		Example: `  $ apigw api conn-pool set billing --conns 64 --timeout 30s --requests 5000
  $ apigw api conn-pool show billing
  $ apigw api conn-pool clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var conns, requests int
	var timeoutStr string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set keepalive pool overrides (apigw_<api> upstream block)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if conns < 0 || conns > 4096 {
				return fmt.Errorf("--conns must be 0-4096 (got %d)", conns)
			}
			if requests < 0 {
				return fmt.Errorf("--requests must be >= 0 (got %d)", requests)
			}
			pool := &config.ConnectionPool{
				KeepaliveConns:    conns,
				KeepaliveRequests: requests,
			}
			if timeoutStr != "" {
				d, perr := time.ParseDuration(timeoutStr)
				if perr != nil {
					return fmt.Errorf("--timeout %q: %w", timeoutStr, perr)
				}
				if d < 1*time.Second {
					return fmt.Errorf("--timeout must be at least 1s (got %s)", d)
				}
				pool.KeepaliveTimeout = d
			}
			if pool.KeepaliveConns == 0 && pool.KeepaliveTimeout == 0 && pool.KeepaliveRequests == 0 {
				return fmt.Errorf("at least one of --conns / --timeout / --requests must be non-zero (use `clear` to remove)")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.ConnectionPool = pool
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "conn-pool set on %s: conns=%s timeout=%s requests=%s\n",
				args[0], dashIfZero(pool.KeepaliveConns), dashIfZeroDur(pool.KeepaliveTimeout),
				dashIfZero(pool.KeepaliveRequests))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().IntVar(&conns, "conns", 0,
		"max idle upstream connections per worker (0 = inherit default 16)")
	cmd.Flags().StringVar(&timeoutStr, "timeout", "",
		"idle keepalive timeout (Go duration, e.g. 60s; empty = nginx default 60s)")
	cmd.Flags().IntVar(&requests, "requests", 0,
		"max requests per connection before nginx closes it (0 = nginx default 1000)")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the connection-pool overrides configured on an API",
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
			if api.ConnectionPool == nil {
				fmt.Fprintf(out, "%s: no overrides (defaults: conns=16, timeout=60s, requests=1000)\n", args[0])
				return nil
			}
			p := api.ConnectionPool
			fmt.Fprintf(out, "%s conn-pool:\n", args[0])
			fmt.Fprintf(out, "  keepalive conns:    %s\n", dashOr(dashIfZero(p.KeepaliveConns), "(default 16)"))
			fmt.Fprintf(out, "  keepalive timeout:  %s\n", dashOr(dashIfZeroDur(p.KeepaliveTimeout), "(default 60s)"))
			fmt.Fprintf(out, "  keepalive requests: %s\n", dashOr(dashIfZero(p.KeepaliveRequests), "(default 1000)"))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the connection-pool overrides — revert to defaults",
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
			if api.ConnectionPool == nil {
				fmt.Fprintln(f.IOStreams.Out, "no overrides to clear")
				return nil
			}
			api.ConnectionPool = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "conn-pool cleared on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
		},
	}
}

// ---------- helpers ----------

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

func dashIfZero(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func dashIfZeroDur(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return d.String()
}

func dashOr(value, fallback string) string {
	if value == "-" {
		return value + " " + fallback
	}
	return value
}
