// Package audit implements `apigw audit …` — query/export/verify the
// immutable operator audit log.
package audit

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/audit"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdAudit(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit <command>",
		Short: "Operator audit log: query, verify, export",
		Long: "Every operator mutation is recorded in an append-only,\n" +
			"hash-chained log at " + paths.StateDir() + "/audit.db.\n" +
			"Use these commands to investigate changes, export to SIEM,\n" +
			"or verify the chain hasn't been tampered with.",
	}
	cmd.AddCommand(newCmdQuery(f))
	cmd.AddCommand(newCmdVerify(f))
	cmd.AddCommand(newCmdExport(f))
	cmd.AddCommand(newCmdTail(f))
	return cmd
}

func openLogger() (*audit.Logger, error) {
	return audit.Open(paths.StateDir())
}

func newCmdQuery(f *cmdutil.Factory) *cobra.Command {
	var (
		actor, action, resource, result string
		since                           string
		limit                           int
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Filter and list audit entries",
		Example: `  $ apigw audit query --since 7d
  $ apigw audit query --actor alice --action deploy.apply
  $ apigw audit query --result failed --limit 50`,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := openLogger()
			if err != nil {
				return err
			}
			defer l.Close()
			filt := audit.Filter{
				Actor:    actor,
				Action:   action,
				Resource: resource,
				Result:   result,
				Limit:    limit,
			}
			if since != "" {
				t, err := parseSince(since)
				if err != nil {
					return tui.NewError("invalid --since", err.Error())
				}
				filt.Since = t
			}
			fmt.Fprintf(f.IOStreams.Out, "%-6s  %-20s  %-10s  %-20s  %-10s  %s\n",
				"ID", "TIME", "ACTOR", "ACTION", "RESULT", "RESOURCE")
			return l.Query(filt, func(e audit.Entry) bool {
				fmt.Fprintf(f.IOStreams.Out, "%-6d  %-20s  %-10s  %-20s  %-10s  %s\n",
					e.ID,
					e.Timestamp.Format("2006-01-02 15:04:05"),
					trunc(e.Actor, 10),
					trunc(e.Action, 20),
					e.Result,
					e.Resource)
				return true
			})
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "filter by actor")
	cmd.Flags().StringVar(&action, "action", "", "filter by action (e.g. deploy.apply)")
	cmd.Flags().StringVar(&resource, "resource", "", "filter by resource")
	cmd.Flags().StringVar(&result, "result", "", "filter by result (ok|failed|denied)")
	cmd.Flags().StringVar(&since, "since", "", "relative time: 1h, 7d, 30d")
	cmd.Flags().IntVar(&limit, "limit", 100, "max entries (0 = unlimited)")
	return cmd
}

func newCmdVerify(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Re-hash the audit log and report any chain breaks",
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := openLogger()
			if err != nil {
				return err
			}
			defer l.Close()
			broken, err := l.VerifyChain()
			if err != nil {
				return tui.NewError(
					fmt.Sprintf("chain broken at entry #%d", broken),
					err.Error())
			}
			n, _ := l.Count()
			fmt.Fprintf(f.IOStreams.Out, "✓ audit chain intact (%d entries)\n", n)
			return nil
		},
	}
}

func newCmdExport(f *cmdutil.Factory) *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Stream the audit log as JSON-lines (for Splunk/ELK/Sumo)",
		Example: `  $ apigw audit export > audit.jsonl
  $ apigw audit export --since 24h | vector --config siem.toml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := openLogger()
			if err != nil {
				return err
			}
			defer l.Close()
			filt := audit.Filter{}
			if since != "" {
				t, err := parseSince(since)
				if err != nil {
					return tui.NewError("invalid --since", err.Error())
				}
				filt.Since = t
			}
			n, err := l.ExportJSONL(filt, f.IOStreams.Out)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "exported %d entries\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "relative time: 1h, 7d, 30d")
	return cmd
}

func newCmdTail(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "tail",
		Short: "Show the most recent audit entries (last 20)",
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := openLogger()
			if err != nil {
				return err
			}
			defer l.Close()
			entries := []audit.Entry{}
			_ = l.Query(audit.Filter{}, func(e audit.Entry) bool {
				entries = append(entries, e)
				return true
			})
			start := 0
			if len(entries) > 20 {
				start = len(entries) - 20
			}
			fmt.Fprintf(f.IOStreams.Out, "%-6s  %-20s  %-10s  %-20s  %-10s  %s\n",
				"ID", "TIME", "ACTOR", "ACTION", "RESULT", "RESOURCE")
			for _, e := range entries[start:] {
				fmt.Fprintf(f.IOStreams.Out, "%-6d  %-20s  %-10s  %-20s  %-10s  %s\n",
					e.ID, e.Timestamp.Format("2006-01-02 15:04:05"),
					trunc(e.Actor, 10), trunc(e.Action, 20), e.Result, e.Resource)
			}
			return nil
		},
	}
}

func parseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return time.Time{}, fmt.Errorf("expected like 1h, 7d, 30d")
	}
	unit := s[len(s)-1]
	var num int
	if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &num); err != nil {
		return time.Time{}, err
	}
	var d time.Duration
	switch unit {
	case 'h':
		d = time.Duration(num) * time.Hour
	case 'd':
		d = time.Duration(num) * 24 * time.Hour
	case 'm':
		d = time.Duration(num) * time.Minute
	default:
		return time.Time{}, fmt.Errorf("unit must be m/h/d")
	}
	return time.Now().Add(-d), nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
