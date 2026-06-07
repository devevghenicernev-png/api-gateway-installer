// Package variants owns `apigw api variants …` — N-way weighted
// traffic splitting. A generalization of Canary that scales beyond
// two pools — typical use: send 70% to v1, 20% to v2, 10% to v3 so a
// gradual three-version rollout doesn't need a chain of canaries.
//
// CLI verbs:
//
//	apigw api variants set <api> --variant <name>=<weight>=<addr1,addr2,...> [--variant ...]
//	apigw api variants list <api>
//	apigw api variants clear <api>
//
// Mutually exclusive at runtime with Canary and BlueGreen on the
// same API (generator picks Variants when set).
package variants

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdVariants(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "variants <command>",
		Short: "N-way weighted traffic splitting between API versions",
		Long: "A generalization of Canary to more than two pools. Operators " +
			"typically use this for version-based routing: 70% to v1, 20% to " +
			"v2, 10% to v3. Variants take precedence over Canary and " +
			"BlueGreen on the same API.",
		Example: `  $ apigw api variants set billing \
      --variant v1=70=10.0.1.10:3000,10.0.1.11:3000 \
      --variant v2=20=10.0.2.10:3000 \
      --variant v3=10=10.0.3.10:3000
  $ apigw api variants list billing
  $ apigw api variants clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var raw []string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Replace the variants list on an API",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(raw) < 2 {
				return fmt.Errorf("--variant must be passed at least twice (a single variant isn't a split)")
			}
			vs, err := parseVariants(raw)
			if err != nil {
				return err
			}
			total := 0
			for _, v := range vs {
				total += v.Weight
			}
			if total > 100 {
				return fmt.Errorf("variant weights sum to %d (>100); they should sum to ~100", total)
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.Variants = vs
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "variants set on %s: %d variant(s), weights sum %d%%\n",
				args[0], len(vs), total)
			if total < 100 {
				fmt.Fprintf(f.IOStreams.Out,
					"  note: last variant %q gets the wildcard catch (+%d%%) so rounding doesn't drop traffic.\n",
					vs[len(vs)-1].Name, 100-total)
			}
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&raw, "variant", nil,
		`one variant per flag: --variant <name>=<weight>=<addr1,addr2,...>`)
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list <api>",
		Short: "Show the configured variants and their weights",
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
			if len(api.Variants) == 0 {
				fmt.Fprintf(out, "%s: no variants\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%-16s  %-8s  %s\n", "NAME", "WEIGHT", "UPSTREAMS")
			vs := append([]config.Variant(nil), api.Variants...)
			sort.SliceStable(vs, func(i, j int) bool { return vs[i].Weight > vs[j].Weight })
			for _, v := range vs {
				fmt.Fprintf(out, "%-16s  %3d%%      %s\n", v.Name, v.Weight, formatPool(v.Upstreams))
			}
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the variants list — traffic returns to the primary upstream",
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
			if len(api.Variants) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no variants to clear")
				return nil
			}
			api.Variants = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "variants cleared on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
		},
	}
}

// ---------- helpers ----------

// parseVariants takes raw `--variant` strings of the form
// `name=weight=addr1,addr2` and returns the typed list. Strict —
// any mis-shaped spec is an error so operators don't silently lose
// pools.
func parseVariants(raw []string) ([]config.Variant, error) {
	out := make([]config.Variant, 0, len(raw))
	for i, r := range raw {
		parts := strings.SplitN(r, "=", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("--variant #%d (%q): expected name=weight=addr1,addr2,...", i+1, r)
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			return nil, fmt.Errorf("--variant #%d: empty name", i+1)
		}
		w, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("--variant %q: weight %q: %w", name, parts[1], err)
		}
		if w < 1 || w > 100 {
			return nil, fmt.Errorf("--variant %q: weight %d must be 1-100", name, w)
		}
		var ups []config.Upstream
		for _, addr := range strings.Split(parts[2], ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			ups = append(ups, config.Upstream{Address: addr})
		}
		if len(ups) == 0 {
			return nil, fmt.Errorf("--variant %q: at least one upstream address required", name)
		}
		out = append(out, config.Variant{Name: name, Weight: w, Upstreams: ups})
	}
	// Guard against name collisions — split_clients targets must be
	// distinct or nginx aborts on reload.
	seen := map[string]struct{}{}
	for _, v := range out {
		if _, dup := seen[v.Name]; dup {
			return nil, fmt.Errorf("duplicate variant name %q", v.Name)
		}
		seen[v.Name] = struct{}{}
	}
	return out, nil
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

func formatPool(us []config.Upstream) string {
	if len(us) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(us))
	for _, u := range us {
		parts = append(parts, u.Address)
	}
	return strings.Join(parts, ", ")
}
