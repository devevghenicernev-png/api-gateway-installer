// Package session owns `apigw auth session …` — list, revoke, and
// clean up cookie-based sessions. Issuing is intentionally NOT a CLI
// verb: sessions are minted from an upstream login flow (OAuth2
// callback, SAML callback, custom backend) via
// POST /api/admin/sessions. The CLI is the operator-facing housekeeping
// surface.
package session

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/auth"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/paths"
)

func defaultSessionsPath() string {
	return filepath.Join(paths.StateDir(), "sessions.db")
}

func NewCmdSession(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session <command>",
		Short: "Inspect and revoke cookie-based sessions",
		Long: "Sessions are minted by an upstream login flow (OAuth2 callback, " +
			"SAML callback, or a custom backend) via POST /api/admin/sessions. " +
			"This CLI is the operator-facing housekeeping surface — list, " +
			"revoke by ID, revoke all of a subject's sessions, drop expired rows.",
	}
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRevoke(f))
	cmd.AddCommand(newRevokeSubject(f))
	cmd.AddCommand(newCleanup(f))
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	var subject string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions (optionally filtered by --subject)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			store, cleanup, err := openStore(f)
			if err != nil {
				return err
			}
			defer cleanup()
			rows, err := store.List(subject, limit)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			if len(rows) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no sessions")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-44s  %-32s  %-19s  %-19s  %s\n",
				"ID", "SUBJECT", "ISSUED", "EXPIRES", "SCOPES")
			for _, r := range rows {
				scopes := "-"
				if len(r.Scopes) > 0 {
					scopes = strings.Join(r.Scopes, ",")
				}
				fmt.Fprintf(out, "%-44s  %-32s  %-19s  %-19s  %s\n",
					redactID(r.ID), trunc(r.Subject, 32),
					r.IssuedAt.Format("2006-01-02 15:04:05"),
					r.ExpiresAt.Format("2006-01-02 15:04:05"),
					scopes)
			}
			fmt.Fprintf(out, "\n%d total in store (across all subjects)\n", store.Size())
			return nil
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "", "filter to one subject (user ID)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
	return cmd
}

func newRevoke(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <session-id>",
		Short: "Revoke a single session by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store, cleanup, err := openStore(f)
			if err != nil {
				return err
			}
			defer cleanup()
			if err := store.Revoke(args[0]); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "revoked %s\n", redactID(args[0]))
			return nil
		},
	}
}

func newRevokeSubject(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-subject <subject>",
		Short: "Revoke every session for one subject (log out everywhere)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store, cleanup, err := openStore(f)
			if err != nil {
				return err
			}
			defer cleanup()
			n, err := store.RevokeBySubject(args[0])
			if err != nil {
				return fmt.Errorf("revoke-subject: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "revoked %d session(s) for %s\n", n, args[0])
			return nil
		},
	}
}

func newCleanup(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Drop every session whose ExpiresAt has passed",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			store, cleanup, err := openStore(f)
			if err != nil {
				return err
			}
			defer cleanup()
			n, err := store.Cleanup()
			if err != nil {
				return fmt.Errorf("cleanup: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "dropped %d expired session(s)\n", n)
			return nil
		},
	}
}

// ---------- helpers ----------

func openStore(f *cmdutil.Factory) (*auth.SessionStore, func(), error) {
	c, err := f.Config()
	if err != nil {
		return nil, nil, err
	}
	cfg, ok := c.(*config.Config)
	if !ok {
		return nil, nil, fmt.Errorf("config: unexpected type %T", c)
	}
	path := defaultSessionsPath()
	if cfg.Security.StateDir != "" {
		path = cfg.Security.StateDir + "/sessions.db"
	}
	store, err := auth.OpenSessionStore(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	return store, func() { _ = store.Close() }, nil
}

func redactID(id string) string {
	if len(id) < 16 {
		return id
	}
	return id[:8] + "…" + id[len(id)-6:]
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
