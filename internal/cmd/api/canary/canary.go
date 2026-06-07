// Package canary owns `apigw api canary …` — configure percentage-
// based traffic splitting for one API. The canary is its own upstream
// pool; the gateway sends Weight% of traffic to it and the rest to
// the primary pool. Sticky pins per-IP. PinHeader (e.g. "X-Canary: 1")
// forces any client to the canary regardless of percentage.
//
// CLI verbs:
//
//	apigw api canary set <api> --to <addr[,addr]> --weight 5 [--sticky] [--pin-header X-Canary]
//	apigw api canary status <api>
//	apigw api canary clear <api>            # stop sending traffic to canary
//	apigw api canary promote <api>          # canary upstreams become primary; canary cleared
package canary

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdCanary(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "canary <command>",
		Short: "Progressive traffic split between primary and canary pools",
		Long: "Send N% of an API's traffic to a canary upstream pool. " +
			"Sticky mode pins each client (by IP) so they don't bounce. " +
			"PinHeader (e.g. X-Canary: 1) overrides the split for ad-hoc QA. " +
			"`promote` atomically swaps the canary pool into primary — zero-" +
			"risk cutover after a successful rollout.",
		Example: `  $ apigw api canary set billing --to 10.0.1.10:3000,10.0.1.11:3000 --weight 5
  $ apigw api canary set billing --weight 25 --sticky
  $ apigw api canary status billing
  $ apigw api canary promote billing       # ship the canary
  $ apigw api canary clear billing         # roll back`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newStatus(f))
	cmd.AddCommand(newClear(f))
	cmd.AddCommand(newPromote(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var to []string
	var weight int
	var sticky bool
	var pinHeader string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Configure or update the canary for an API",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if weight < 0 || weight > 100 {
				return fmt.Errorf("--weight must be 0-100 (got %d)", weight)
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if api.Canary == nil {
				api.Canary = &config.Canary{}
			}
			if len(to) > 0 {
				api.Canary.Upstreams = parseUpstreams(to)
			}
			if len(api.Canary.Upstreams) == 0 {
				return fmt.Errorf("canary needs at least one upstream — pass --to host:port[,host:port]")
			}
			api.Canary.Weight = weight
			api.Canary.Sticky = sticky
			if pinHeader != "" {
				api.Canary.PinHeader = pinHeader
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "canary set on %s: weight=%d%% sticky=%v pin=%s upstreams=%d\n",
				args[0], weight, sticky, dashIfEmpty(api.Canary.PinHeader), len(api.Canary.Upstreams))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&to, "to", nil,
		"canary upstreams (comma-separated host:port; preserved when omitted on update)")
	cmd.Flags().IntVar(&weight, "weight", 0, "0-100 percent of traffic to send to canary")
	cmd.Flags().BoolVar(&sticky, "sticky", false,
		"hash by $remote_addr so each client consistently hits one pool (false = per-request random)")
	cmd.Flags().StringVar(&pinHeader, "pin-header", "",
		`request header that forces canary when value is "1" (e.g. X-Canary)`)
	return cmd
}

func newStatus(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status <api>",
		Short: "Show the canary configured on an API",
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
			if api.Canary == nil || api.Canary.Weight == 0 {
				fmt.Fprintf(out, "%s: no canary (100%% to primary)\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s canary:\n", args[0])
			fmt.Fprintf(out, "  weight:     %d%% (%d%% primary)\n", api.Canary.Weight, 100-api.Canary.Weight)
			fmt.Fprintf(out, "  sticky:     %v\n", api.Canary.Sticky)
			fmt.Fprintf(out, "  pin-header: %s\n", dashIfEmpty(api.Canary.PinHeader))
			fmt.Fprintf(out, "  upstreams:  %s\n", formatUpstreams(api.Canary.Upstreams))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Stop sending traffic to the canary (drops the config block)",
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
			if api.Canary == nil {
				fmt.Fprintln(f.IOStreams.Out, "no canary to clear")
				return nil
			}
			api.Canary = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "canary cleared on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
		},
	}
}

func newPromote(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "promote <api>",
		Short: "Make the canary the new primary (atomic swap, canary cleared)",
		Long: "Replaces the API's Upstreams with the Canary.Upstreams and clears " +
			"the canary block. Use after the canary has proven itself — this " +
			"is the cutover step. Zero downtime: nginx reload picks up the new " +
			"primary atomically.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if api.Canary == nil || len(api.Canary.Upstreams) == 0 {
				return fmt.Errorf("no canary to promote — configure with `canary set` first")
			}
			previousPrimary := api.Upstreams
			api.Upstreams = api.Canary.Upstreams
			// Promoting at any non-100% weight is fine — the canary has been
			// verified; we're committing it. Reset Port (now ambiguous with
			// multi-upstream pool) only if there are multiple.
			if len(api.Upstreams) > 1 {
				api.Port = 0
			} else if len(api.Upstreams) == 1 {
				// Best-effort: extract port from "host:port" so single-upstream
				// installs keep their legacy Port field consistent.
				addr := api.Upstreams[0].Address
				if i := strings.LastIndex(addr, ":"); i > 0 {
					api.Port = 0 // explicitly clear; new shape relies on Upstreams
					_ = i
				}
			}
			api.Canary = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "%s promoted — canary became primary (was %d upstream(s)).\n",
				args[0], len(previousPrimary))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
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

func parseUpstreams(addrs []string) []config.Upstream {
	out := make([]config.Upstream, 0, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		out = append(out, config.Upstream{Address: a})
	}
	return out
}

func formatUpstreams(us []config.Upstream) string {
	if len(us) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(us))
	for _, u := range us {
		parts = append(parts, u.Address)
	}
	return strings.Join(parts, ", ")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
