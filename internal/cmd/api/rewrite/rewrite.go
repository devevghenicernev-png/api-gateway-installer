// Package rewrite owns `apigw api rewrite …` — per-route URL
// rewriting via nginx's `rewrite <regex> <replace> <flag>` directive.
// Multiple rules evaluate in order until a `last`, `redirect`, or
// `permanent` flag short-circuits.
package rewrite

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

var validFlags = []string{"last", "break", "redirect", "permanent"}

func NewCmdRewrite(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rewrite <command>",
		Short: "URL rewriting (nginx rewrite directive) per route",
		Example: `  $ apigw api rewrite add billing --match '^/v1/(.*)$' --replace '/v2/$1' --flag last
  $ apigw api rewrite add billing --match '^/old$' --replace 'https://new.example.com' --flag permanent
  $ apigw api rewrite list billing
  $ apigw api rewrite remove billing 0`,
	}
	cmd.AddCommand(newAdd(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRemove(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newAdd(f *cmdutil.Factory) *cobra.Command {
	var match, replace, flag string
	cmd := &cobra.Command{
		Use:   "add <api>",
		Short: "Append a rewrite rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if match == "" || replace == "" {
				return fmt.Errorf("--match and --replace are required")
			}
			if !validFlag(flag) {
				return fmt.Errorf("--flag must be one of: %v", validFlags)
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.Rewrites = append(api.Rewrites, config.RewriteRule{
				Match: match, Replace: replace, Flag: flag,
			})
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "rewrite added on %s (#%d) — run `apigw api reload` to apply.\n",
				args[0], len(api.Rewrites)-1)
			return nil
		},
	}
	cmd.Flags().StringVar(&match, "match", "", "regex matched against the request URI")
	cmd.Flags().StringVar(&replace, "replace", "", "replacement string (supports $1, $2 captures; or full URL for redirect/permanent)")
	cmd.Flags().StringVar(&flag, "flag", "last", "nginx rewrite flag: last|break|redirect|permanent")
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list <api>",
		Short: "List rewrite rules (with their indices)",
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
			if len(api.Rewrites) == 0 {
				fmt.Fprintf(out, "%s: no rewrite rules\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%-4s  %-10s  %-30s  %s\n", "IDX", "FLAG", "MATCH", "REPLACE")
			for i, r := range api.Rewrites {
				flag := r.Flag
				if flag == "" {
					flag = "last"
				}
				fmt.Fprintf(out, "%-4d  %-10s  %-30s  %s\n", i, flag, r.Match, r.Replace)
			}
			return nil
		},
	}
}

func newRemove(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <api> <index>",
		Short: "Remove one rewrite rule by index (see `list`)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			var idx int
			if _, err := fmt.Sscanf(args[1], "%d", &idx); err != nil {
				return fmt.Errorf("index must be an integer (got %q)", args[1])
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if idx < 0 || idx >= len(api.Rewrites) {
				return fmt.Errorf("index %d out of range (have %d rule(s))", idx, len(api.Rewrites))
			}
			api.Rewrites = append(api.Rewrites[:idx], api.Rewrites[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "rewrite #%d removed on %s — run `apigw api reload` to apply.\n", idx, args[0])
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove every rewrite rule",
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
			if len(api.Rewrites) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no rules to clear")
				return nil
			}
			n := len(api.Rewrites)
			api.Rewrites = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "cleared %d rewrite rule(s) on %s — run `apigw api reload` to apply.\n", n, args[0])
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

func validFlag(f string) bool {
	if f == "" {
		return true
	}
	i := sort.SearchStrings(validFlagsSorted, f)
	return i < len(validFlagsSorted) && validFlagsSorted[i] == f
}

var validFlagsSorted = func() []string {
	out := append([]string(nil), validFlags...)
	sort.Strings(out)
	return out
}()
