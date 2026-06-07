// Package retry owns `apigw api retry …` — per-route retry policy
// (proxy_next_upstream + tries + timeouts). Conditions and on-status
// merge into the nginx proxy_next_upstream list; attempts caps the
// number of upstreams tried; per-try sets proxy_read_timeout per
// attempt and the total budget across all attempts (per-try × attempts).
//
// Note: backoff / jitter aren't exposed — stock nginx retries the
// next upstream immediately. Real backoff needs Lua / njs; planned
// for the plugin runtime work (§8 in the implementation plan).
package retry

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdRetry(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retry <command>",
		Short: "Per-route retry policy (proxy_next_upstream + tries + timeouts)",
		Example: `  $ apigw api retry set billing --attempts 3 --on-status 502,503,504 --per-try 5s
  $ apigw api retry set billing --conditions "error,timeout,non_idempotent"
  $ apigw api retry show billing
  $ apigw api retry clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var conditions []string
	var onStatusRaw []string
	var attempts int
	var perTryStr string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Replace the retry policy on an API",
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
			r := &config.Retry{Conditions: conditions, Attempts: attempts}
			for _, s := range onStatusRaw {
				for _, part := range strings.Split(s, ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					code, perr := strconv.Atoi(part)
					if perr != nil || code < 100 || code > 599 {
						return fmt.Errorf("--on-status %q: must be HTTP status (100-599)", part)
					}
					r.OnStatus = append(r.OnStatus, code)
				}
			}
			if perTryStr != "" {
				d, perr := time.ParseDuration(perTryStr)
				if perr != nil {
					return fmt.Errorf("--per-try %q: %w", perTryStr, perr)
				}
				if d < 1*time.Second {
					return fmt.Errorf("--per-try must be at least 1s (got %s)", d)
				}
				r.PerTry = d
			}
			if r.Attempts < 0 || r.Attempts > 10 {
				return fmt.Errorf("--attempts must be 0-10 (got %d)", r.Attempts)
			}
			api.Retry = r
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "retry set on %s: attempts=%d on-status=%v per-try=%s\n",
				args[0], r.Attempts, r.OnStatus, dashIfZero(r.PerTry))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&conditions, "conditions", nil,
		"raw nginx proxy_next_upstream conditions (error, timeout, non_idempotent, http_500, ...)")
	cmd.Flags().StringSliceVar(&onStatusRaw, "on-status", nil,
		"HTTP statuses to retry on (e.g. --on-status 502,503,504) — merged with --conditions")
	cmd.Flags().IntVar(&attempts, "attempts", 3, "number of upstreams to try per request (1 = no retry, 0 = default 3)")
	cmd.Flags().StringVar(&perTryStr, "per-try", "", "per-attempt timeout (Go duration, e.g. 5s, 2500ms)")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the retry policy configured on an API",
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
			if api.Retry == nil {
				fmt.Fprintf(out, "%s: no retry policy (nginx defaults: tries=1, no proxy_next_upstream)\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s retry:\n", args[0])
			fmt.Fprintf(out, "  conditions: %s\n", dashIfEmpty(strings.Join(api.Retry.Conditions, ", ")))
			fmt.Fprintf(out, "  on-status:  %v\n", api.Retry.OnStatus)
			fmt.Fprintf(out, "  attempts:   %d\n", api.Retry.Attempts)
			fmt.Fprintf(out, "  per-try:    %s\n", dashIfZero(api.Retry.PerTry))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the retry policy — revert to nginx defaults",
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
			if api.Retry == nil {
				fmt.Fprintln(f.IOStreams.Out, "no retry policy to clear")
				return nil
			}
			api.Retry = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "retry cleared on %s — run `apigw api reload` to apply.\n", args[0])
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

func dashIfEmpty(s string) string {
	if s == "" {
		return "(default: error timeout http_502 http_503 http_504)"
	}
	return s
}

func dashIfZero(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return d.String()
}
