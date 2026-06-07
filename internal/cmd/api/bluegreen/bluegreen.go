// Package bluegreen owns `apigw api blue-green …` — atomic
// two-pool deployments. Two named upstream pools (blue and green)
// live in config; one is Active and gets 100% of traffic, the other
// is staging. Swap flips Active in one config save → one nginx
// reload. Rollback = swap again.
//
// CLI verbs:
//
//	apigw api blue-green set <api> --blue host:port,...  --green host:port,...  --active blue
//	apigw api blue-green swap <api>            # flip active pool, reload
//	apigw api blue-green status <api>
//	apigw api blue-green clear <api>           # remove the BG block, revert to API.Upstreams
package bluegreen

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdBlueGreen(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "blue-green <command>",
		Aliases: []string{"bluegreen", "bg"},
		Short:   "Atomic two-pool deployments — deploy to staging pool, swap, reload",
		Long: "Maintain two named pools (blue + green). One is Active; the " +
			"other receives no traffic and is your staging target. After you " +
			"deploy + smoke-test the staging pool, `blue-green swap` makes it " +
			"active in one nginx reload. Rollback is another swap — both pools " +
			"stay declared in config.\n\n" +
			"Drain note: stock nginx has no slow_start. A swap is instant. If " +
			"upstream sessions need draining, set the staging pool's weights to " +
			"0 first, reload, wait, then swap. nginx Plus has slow_start for " +
			"automatic ramp-up.",
		Example: `  $ apigw api blue-green set billing --blue 10.0.1.10:3000 --green 10.0.2.10:3000 --active blue
  $ apigw api blue-green swap billing
  $ apigw api blue-green status billing
  $ apigw api blue-green clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newSwap(f))
	cmd.AddCommand(newStatus(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var blue, green []string
	var active string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Configure both pools and pick which one starts active",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if active != "blue" && active != "green" {
				return fmt.Errorf("--active must be 'blue' or 'green'")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if api.BlueGreen == nil {
				api.BlueGreen = &config.BlueGreen{}
			}
			if len(blue) > 0 {
				api.BlueGreen.Blue = parseUpstreams(blue)
			}
			if len(green) > 0 {
				api.BlueGreen.Green = parseUpstreams(green)
			}
			api.BlueGreen.Active = active
			if len(api.BlueGreen.Blue) == 0 || len(api.BlueGreen.Green) == 0 {
				return fmt.Errorf("both --blue and --green pools must be non-empty (set on first use)")
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "blue-green set on %s: active=%s blue=%d green=%d\n",
				args[0], active, len(api.BlueGreen.Blue), len(api.BlueGreen.Green))
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&blue, "blue", nil, "blue pool upstreams (comma-separated host:port)")
	cmd.Flags().StringSliceVar(&green, "green", nil, "green pool upstreams (comma-separated host:port)")
	cmd.Flags().StringVar(&active, "active", "blue", "which pool receives traffic: 'blue' or 'green'")
	return cmd
}

func newSwap(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "swap <api>",
		Short: "Flip the active pool (blue↔green); rollback = swap again",
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
			if api.BlueGreen == nil {
				return fmt.Errorf("no blue-green config on %q — run `set` first", args[0])
			}
			from := api.BlueGreen.Active
			switch from {
			case "blue":
				api.BlueGreen.Active = "green"
			case "green":
				api.BlueGreen.Active = "blue"
			default:
				return fmt.Errorf("active is %q — expected 'blue' or 'green'", from)
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "swapped %s: %s → %s\n", args[0], from, api.BlueGreen.Active)
			fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
			return nil
		},
	}
}

func newStatus(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status <api>",
		Short: "Show pool config and which one is active",
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
			if api.BlueGreen == nil {
				fmt.Fprintf(out, "%s: no blue-green configured\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s blue-green (active: %s):\n", args[0], api.BlueGreen.Active)
			fmt.Fprintf(out, "  blue:  %s%s\n", formatPool(api.BlueGreen.Blue), activeMark(api.BlueGreen.Active == "blue"))
			fmt.Fprintf(out, "  green: %s%s\n", formatPool(api.BlueGreen.Green), activeMark(api.BlueGreen.Active == "green"))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the blue-green config; revert to API.Upstreams",
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
			if api.BlueGreen == nil {
				fmt.Fprintln(f.IOStreams.Out, "no blue-green to clear")
				return nil
			}
			if len(api.Upstreams) == 0 && api.Port == 0 {
				return fmt.Errorf("cleared blue-green would leave the API with no upstream — set API.Upstreams or Port first")
			}
			api.BlueGreen = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "blue-green cleared on %s — run `apigw api reload` to apply.\n", args[0])
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

func formatPool(us []config.Upstream) string {
	if len(us) == 0 {
		return "(empty)"
	}
	parts := make([]string, 0, len(us))
	for _, u := range us {
		parts = append(parts, u.Address)
	}
	return strings.Join(parts, ", ")
}

func activeMark(active bool) string {
	if active {
		return "   ← serving"
	}
	return ""
}
