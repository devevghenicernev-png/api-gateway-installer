// Package admin owns the `apigw auth init / token / enforce` commands —
// the operator-facing CLI around the admin-API security layer.
//
// Why a separate package: the existing `apigw auth jwt|oidc|mtls`
// commands target per-API user-authentication (browser/clients hitting
// nginx). The admin-API security layer is something else entirely:
// who can mutate the gateway's config. Keeping them apart avoids
// confusing operators about "which auth do I configure for what".
package admin

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

// NewCmdAdmin returns the `apigw auth admin` subtree. Mounted under the
// existing `apigw auth …` parent in internal/cmd/auth/auth.go.
func NewCmdAdmin(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin <command>",
		Short: "Manage admin-API tokens, RBAC enforcement, approvals",
		Long: "Bootstrap and rotate tokens that authorise mutations to /api/admin/*.\n" +
			"Use `init` once on a fresh install; `token add` afterwards for CI bots\n" +
			"or extra operators; `enforce` once you've watched the audit log for a few\n" +
			"days and are confident no day-to-day work is being silently denied.",
	}
	cmd.AddCommand(newCmdInit(f))
	cmd.AddCommand(newCmdToken(f))
	cmd.AddCommand(newCmdEnforce(f))
	cmd.AddCommand(newCmdSoft(f))
	return cmd
}

// newCmdToken is the `apigw auth admin token` umbrella.
func newCmdToken(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token <command>",
		Short: "Manage admin tokens (add / list / revoke)",
	}
	cmd.AddCommand(newCmdTokenAdd(f))
	cmd.AddCommand(newCmdTokenList(f))
	cmd.AddCommand(newCmdTokenRevoke(f))
	return cmd
}

// -------- init --------

func newCmdInit(f *cmdutil.Factory) *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap admin API: generate an owner token, enable RBAC in soft mode",
		Long: "Generates a 32-byte token (base64-url, no padding) and adds it to\n" +
			"`security.admin_tokens` with role `owner`. RBAC is enabled in\n" +
			"soft-rollout mode — every request is allowed but denials are\n" +
			"audit-logged. Promote to hard-enforce with `apigw auth admin enforce`\n" +
			"once you've watched the audit log.\n\n" +
			"Idempotent — re-running prints the existing token if one already\n" +
			"matches the requested user.",
		Example: `  $ sudo apigw auth admin init
  $ sudo apigw auth admin init --user ops`,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			// If an owner-role token for this user exists, skip.
			for _, t := range cfg.Security.AdminTokens {
				if t.User == user {
					return alreadyInitialised(f, user)
				}
			}
			token, err := generateToken()
			if err != nil {
				return err
			}
			cfg.Security.AdminTokens = append(cfg.Security.AdminTokens, config.AdminToken{
				Name:  user + "-owner",
				User:  user,
				Token: token,
			})
			if !hasAssignment(cfg.Security.Assignments, user) {
				cfg.Security.Assignments = append(cfg.Security.Assignments, config.Assignment{
					User:  user,
					Roles: []string{"owner"},
				})
			}
			// Soft-rollout: don't hard-deny on first install.
			// Operator promotes with `apigw auth admin enforce`.
			cfg.Security.RBACEnforce = false
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			printToken(f, user, token, "init")
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "admin", "RBAC subject identity for this token")
	return cmd
}

// -------- token add --------

func newCmdTokenAdd(f *cmdutil.Factory) *cobra.Command {
	var user, name string
	var groups []string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Generate a new admin-API token",
		Args:  cobra.ExactArgs(1),
		Long: "Adds a token under config.security.admin_tokens. The bearer of\n" +
			"this token will be identified as `--user` and granted whatever\n" +
			"roles are assigned to that user via `security.assignments`.\n\n" +
			"If the user has no assignment yet, the token works but every\n" +
			"action is denied (until you `apigw auth admin token grant`).",
		Example: `  $ sudo apigw auth admin token add ci-deploy --user ci --groups operators
  $ sudo apigw auth admin token add backup-bot --user backup`,
		RunE: func(c *cobra.Command, args []string) error {
			name = args[0]
			if user == "" {
				user = name
			}
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			for _, t := range cfg.Security.AdminTokens {
				if t.Name == name {
					return fmt.Errorf("token %q already exists; revoke it first", name)
				}
			}
			token, err := generateToken()
			if err != nil {
				return err
			}
			cfg.Security.AdminTokens = append(cfg.Security.AdminTokens, config.AdminToken{
				Name:   name,
				User:   user,
				Token:  token,
				Groups: groups,
			})
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			printToken(f, user, token, "token add")
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "RBAC subject (defaults to <name>)")
	cmd.Flags().StringSliceVar(&groups, "groups", nil, "LDAP/SAML-style groups")
	return cmd
}

// -------- token list --------

func newCmdTokenList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List admin tokens (token values are redacted)",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			ios := f.IOStreams
			if len(cfg.Security.AdminTokens) == 0 {
				fmt.Fprintln(ios.Out, "no admin tokens — run `apigw auth admin init`")
				return nil
			}
			fmt.Fprintf(ios.Out, "%-24s %-16s %-6s %s\n", "NAME", "USER", "GROUPS", "TOKEN")
			for _, t := range cfg.Security.AdminTokens {
				groups := strings.Join(t.Groups, ",")
				if groups == "" {
					groups = "-"
				}
				fmt.Fprintf(ios.Out, "%-24s %-16s %-6s %s\n",
					t.Name, t.User, groups, redact(t.Token))
			}
			return nil
		},
	}
}

// -------- token revoke --------

func newCmdTokenRevoke(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "Remove an admin token by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			idx := -1
			for i, t := range cfg.Security.AdminTokens {
				if t.Name == name {
					idx = i
					break
				}
			}
			if idx < 0 {
				return fmt.Errorf("no token named %q", name)
			}
			cfg.Security.AdminTokens = append(cfg.Security.AdminTokens[:idx], cfg.Security.AdminTokens[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "revoked %s\n", name)
			return nil
		},
	}
}

// -------- enforce / soft --------

func newCmdEnforce(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "enforce",
		Short: "Switch RBAC to hard-deny (rbac_enforce: true)",
		Long: "After running this, every request without a permission match is\n" +
			"rejected with 403. Recommended ONLY after you've watched the audit\n" +
			"log under soft-rollout for at least a few days — denied actions in\n" +
			"the audit log surface workflows that would suddenly break.",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			cfg.Security.RBACEnforce = true
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintln(f.IOStreams.Out, "RBAC enforce → ON. Restart dashboard to apply.")
			return nil
		},
	}
}

func newCmdSoft(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "soft",
		Short: "Switch RBAC back to soft-rollout (log denials, don't refuse)",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadConfig(f)
			if err != nil {
				return err
			}
			cfg.Security.RBACEnforce = false
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintln(f.IOStreams.Out, "RBAC enforce → OFF (soft rollout). Restart dashboard to apply.")
			return nil
		},
	}
}

// -------- helpers --------

func loadConfig(f *cmdutil.Factory) (*config.Config, error) {
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

// generateToken returns a 32-byte (256-bit) cryptographically random
// secret encoded as base64-URL without padding — safe to drop into an
// Authorization header and into YAML without quoting.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hasAssignment(as []config.Assignment, user string) bool {
	for _, a := range as {
		if a.User == user {
			return true
		}
	}
	return false
}

func redact(s string) string {
	if len(s) < 8 {
		return "********"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func alreadyInitialised(f *cmdutil.Factory, user string) error {
	fmt.Fprintf(f.IOStreams.ErrOut,
		"User %q already has an admin token. Use\n"+
			"  apigw auth admin token list\n"+
			"to inspect, or\n"+
			"  apigw auth admin token revoke <name>\n"+
			"to drop and re-init.\n", user)
	return fmt.Errorf("already initialised")
}

// printToken shows the secret ONCE, with operator-facing instructions.
// We never re-print on subsequent runs — operators are expected to copy
// it into a password manager or CI secret store at this moment.
func printToken(f *cmdutil.Factory, user, token, source string) {
	out := f.IOStreams.Out
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ┌──────────────────────────────────────────────────────────────┐")
	fmt.Fprintln(out, "  │  apigw admin token issued — copy NOW, it won't be shown again │")
	fmt.Fprintln(out, "  └──────────────────────────────────────────────────────────────┘")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    user:   %s\n", user)
	fmt.Fprintf(out, "    token:  %s\n", token)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Use it like:")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    curl -H 'Authorization: Bearer %s' \\\n", token)
	fmt.Fprintln(out, "         http://<dashboard-host>:9080/api/admin/apis")
	fmt.Fprintln(out)
	if source == "init" {
		fmt.Fprintln(out, "  RBAC is in SOFT-ROLLOUT mode — denials are audit-logged but")
		fmt.Fprintln(out, "  the request still goes through. After a few days of clean")
		fmt.Fprintln(out, "  audit logs, run:")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "    sudo apigw auth admin enforce")
		fmt.Fprintln(out)
	}
}
