// Package acl owns `apigw api acl …` — per-API allow / deny gate that
// runs AFTER authentication succeeds. The authenticated identity
// (api-key ID, JWT subject, mTLS CN, etc.) is matched against the
// configured lists; deny-wins precedence.
package acl

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

const (
	matchAPIKeyID = "apikey-id"
	matchHMACID   = "hmac-id"
	matchSubject  = "subject"
	matchMTLSCN   = "mtls-cn"
)

var validMatches = []string{matchAPIKeyID, matchHMACID, matchSubject, matchMTLSCN}

// NewCmdACL returns `apigw api acl <subcommand>`.
func NewCmdACL(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acl <command>",
		Short: "Per-API allow / deny gate that runs after authentication",
		Long: "Filter authenticated identities at the route. After an auth method " +
			"(API key, HMAC, JWT, OAuth2, mTLS) confirms a request, the ACL " +
			"checks whether the matched identity (key ID, sub, CN) passes the " +
			"per-route allow/deny lists. Deny entries always win.\n\n" +
			"Match values: " + strings.Join(validMatches, ", "),
		Example: `  $ apigw api acl set billing --match apikey-id --allow ci-bot,ops --deny rotated-key
  $ apigw api acl set audit --match subject --allow alice@corp.io,bob@corp.io
  $ apigw api acl show billing
  $ apigw api acl clear billing`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var match string
	var allow, deny []string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set the ACL for an API (replaces any existing rule)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if match == "" {
				return fmt.Errorf("--match is required (one of: %s)", strings.Join(validMatches, ", "))
			}
			if !validMatch(match) {
				return fmt.Errorf("invalid --match %q (must be one of: %s)", match, strings.Join(validMatches, ", "))
			}
			if len(allow) == 0 && len(deny) == 0 {
				return fmt.Errorf("at least one of --allow / --deny must be non-empty (use `acl clear` to remove)")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.ACL = &config.ACL{
				Match: match,
				Allow: allow,
				Deny:  deny,
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "acl set on %s: match=%s allow=[%s] deny=[%s]\n",
				args[0], match, strings.Join(allow, ","), strings.Join(deny, ","))
			return nil
		},
	}
	cmd.Flags().StringVar(&match, "match", "",
		"identity field to filter: apikey-id | hmac-id | subject | mtls-cn")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "comma-separated allow-list (empty = no allow constraint)")
	cmd.Flags().StringSliceVar(&deny, "deny", nil, "comma-separated deny-list (always blocks)")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the ACL configured on an API",
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
			if api.ACL == nil {
				fmt.Fprintf(out, "%s: no ACL configured (all authenticated requests pass)\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s ACL:\n", args[0])
			fmt.Fprintf(out, "  match: %s\n", api.ACL.Match)
			fmt.Fprintf(out, "  allow: %s\n", formatList(api.ACL.Allow))
			fmt.Fprintf(out, "  deny:  %s\n", formatList(api.ACL.Deny))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove the ACL from an API (everyone authenticated passes)",
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
			if api.ACL == nil {
				fmt.Fprintln(f.IOStreams.Out, "no ACL to clear")
				return nil
			}
			api.ACL = nil
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "acl cleared on %s\n", args[0])
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

func validMatch(m string) bool {
	for _, v := range validMatches {
		if v == m {
			return true
		}
	}
	return false
}

func formatList(xs []string) string {
	if len(xs) == 0 {
		return "(empty)"
	}
	return strings.Join(xs, ", ")
}
