// Package consumer owns `apigw consumer …` — manage the global
// registry of identities (Consumer) and the named buckets they
// belong to (ConsumerGroup). Credentials reference consumers via
// APIKey.ConsumerID / HMACKey.ConsumerID; JWT/OAuth2/Session/mTLS
// use the auth-supplied subject / CN directly as the consumer ID.
//
// CLI verbs:
//
//	apigw consumer add <id> [--name X] [--groups a,b]
//	apigw consumer list
//	apigw consumer remove <id>
//	apigw consumer join <id> <group>
//	apigw consumer leave <id> <group>
//	apigw consumer group create <name> [--description X]
//	apigw consumer group list
//	apigw consumer group remove <name>
package consumer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdConsumer(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consumer <command>",
		Short: "Manage consumers (identity registry) and consumer groups",
		Long: "Consumers bundle credentials (API keys, HMAC keys, JWT subjects, " +
			"mTLS CNs) into named identities. Groups stack on top and make per-route " +
			"ACL succinct (`match=consumer-group, allow=[ops, ci]`). After an auth " +
			"method succeeds the dashboard sets X-Apigw-Consumer-Id and " +
			"X-Apigw-Consumer-Groups headers for the upstream.",
		Example: `  $ apigw consumer add ci-team --groups ci,internal
  $ apigw consumer list
  $ apigw consumer join ci-team release
  $ apigw consumer group create ops --description "operations team"`,
	}
	cmd.AddCommand(newAdd(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRemove(f))
	cmd.AddCommand(newJoin(f))
	cmd.AddCommand(newLeave(f))
	cmd.AddCommand(newGroup(f))
	return cmd
}

func newAdd(f *cmdutil.Factory) *cobra.Command {
	var name, description string
	var groups []string
	cmd := &cobra.Command{
		Use:   "add <id>",
		Short: "Register a new consumer",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			id := args[0]
			for _, existing := range cfg.Security.Consumers {
				if existing.ID == id {
					return fmt.Errorf("consumer %q already exists", id)
				}
			}
			cfg.Security.Consumers = append(cfg.Security.Consumers, config.Consumer{
				ID:          id,
				Name:        name,
				Groups:      groups,
				Description: description,
			})
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "consumer added: %s (groups: %s)\n", id, fmtGroups(groups))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human-readable display name")
	cmd.Flags().StringVar(&description, "description", "", "free-form description")
	cmd.Flags().StringSliceVar(&groups, "groups", nil, "comma-separated group names")
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered consumers",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			if len(cfg.Security.Consumers) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no consumers configured")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-24s  %-24s  %s\n", "ID", "NAME", "GROUPS")
			rows := append([]config.Consumer(nil), cfg.Security.Consumers...)
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			for _, r := range rows {
				fmt.Fprintf(out, "%-24s  %-24s  %s\n", r.ID, dash(r.Name), fmtGroups(r.Groups))
			}
			return nil
		},
	}
}

func newRemove(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a consumer from the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			idx := findConsumer(cfg, args[0])
			if idx < 0 {
				return fmt.Errorf("consumer %q not found", args[0])
			}
			cfg.Security.Consumers = append(cfg.Security.Consumers[:idx], cfg.Security.Consumers[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "consumer removed: %s\n", args[0])
			return nil
		},
	}
}

func newJoin(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "join <id> <group>",
		Short: "Add a consumer to a group (idempotent)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			idx := findConsumer(cfg, args[0])
			if idx < 0 {
				return fmt.Errorf("consumer %q not found", args[0])
			}
			for _, g := range cfg.Security.Consumers[idx].Groups {
				if g == args[1] {
					fmt.Fprintf(f.IOStreams.Out, "already in group: %s\n", args[1])
					return nil
				}
			}
			cfg.Security.Consumers[idx].Groups = append(cfg.Security.Consumers[idx].Groups, args[1])
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "%s joined %s\n", args[0], args[1])
			return nil
		},
	}
}

func newLeave(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "leave <id> <group>",
		Short: "Remove a consumer from a group (idempotent)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			idx := findConsumer(cfg, args[0])
			if idx < 0 {
				return fmt.Errorf("consumer %q not found", args[0])
			}
			before := cfg.Security.Consumers[idx].Groups
			after := make([]string, 0, len(before))
			for _, g := range before {
				if g != args[1] {
					after = append(after, g)
				}
			}
			if len(after) == len(before) {
				fmt.Fprintf(f.IOStreams.Out, "not in group: %s\n", args[1])
				return nil
			}
			cfg.Security.Consumers[idx].Groups = after
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "%s left %s\n", args[0], args[1])
			return nil
		},
	}
}

// ---------- group ----------

func newGroup(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group <command>",
		Short: "Manage named consumer groups (metadata)",
	}
	cmd.AddCommand(newGroupCreate(f))
	cmd.AddCommand(newGroupList(f))
	cmd.AddCommand(newGroupRemove(f))
	return cmd
}

func newGroupCreate(f *cmdutil.Factory) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Register a group (metadata-only; consumers reference it by name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			for _, g := range cfg.Security.ConsumerGroups {
				if g.Name == args[0] {
					return fmt.Errorf("group %q already exists", args[0])
				}
			}
			cfg.Security.ConsumerGroups = append(cfg.Security.ConsumerGroups, config.ConsumerGroup{
				Name:        args[0],
				Description: description,
			})
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "group created: %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "free-form description")
	return cmd
}

func newGroupList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List declared groups and how many consumers each holds",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			counts := map[string]int{}
			for _, c := range cfg.Security.Consumers {
				for _, g := range c.Groups {
					counts[g]++
				}
			}
			rows := append([]config.ConsumerGroup(nil), cfg.Security.ConsumerGroups...)
			// Surface implicit groups (referenced by consumers but never declared)
			// so operators can spot typos.
			declared := map[string]bool{}
			for _, g := range rows {
				declared[g.Name] = true
			}
			for name := range counts {
				if !declared[name] {
					rows = append(rows, config.ConsumerGroup{Name: name, Description: "(implicit — declare via `consumer group create`)"})
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
			if len(rows) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no groups")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-24s  %-8s  %s\n", "NAME", "MEMBERS", "DESCRIPTION")
			for _, r := range rows {
				fmt.Fprintf(out, "%-24s  %-8d  %s\n", r.Name, counts[r.Name], dash(r.Description))
			}
			return nil
		},
	}
}

func newGroupRemove(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a group declaration (does NOT remove consumer memberships)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			idx := -1
			for i, g := range cfg.Security.ConsumerGroups {
				if g.Name == args[0] {
					idx = i
					break
				}
			}
			if idx < 0 {
				return fmt.Errorf("group %q not declared", args[0])
			}
			cfg.Security.ConsumerGroups = append(cfg.Security.ConsumerGroups[:idx], cfg.Security.ConsumerGroups[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "group declaration removed: %s\n", args[0])
			fmt.Fprintln(f.IOStreams.Out, "  (consumer memberships untouched — clean up with `consumer leave`)")
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

func findConsumer(cfg *config.Config, id string) int {
	for i, c := range cfg.Security.Consumers {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func fmtGroups(g []string) string {
	if len(g) == 0 {
		return "-"
	}
	return strings.Join(g, ",")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
