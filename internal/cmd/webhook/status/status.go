// Package status implements `apigw webhook status`.
package status

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show webhook server status + queue depth + recent deliveries",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			return run(f, asJSON)
		},
	}
	return cmd
}

type out struct {
	SystemdActive bool          `json:"systemd_active"`
	ListenAddr    string        `json:"listen_addr"`
	Path          string        `json:"nginx_path"`
	QueueDepth    int           `json:"queue_depth"`
	DeadLetters   int           `json:"dead_letters"`
	Recent        []webhook.Job `json:"recent"`
}

func run(f *cmdutil.Factory, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	active := exec.Command("systemctl", "is-active", "--quiet", "apigw-webhook.service").Run() == nil

	o := out{
		SystemdActive: active,
		ListenAddr:    fmt.Sprintf(":%d", cfg.Webhook.Port),
		Path:          cfg.Webhook.Path,
	}
	if q, qerr := webhook.OpenQueue(); qerr == nil {
		o.QueueDepth, o.DeadLetters, _ = q.Depth()
		o.Recent, _ = q.RecentDeliveries(5)
		_ = q.Close()
	}

	if asJSON {
		b, _ := json.MarshalIndent(o, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}

	ios := f.IOStreams
	plan := tui.NewPlan("Webhook status").
		Add("Listen", o.ListenAddr).
		Add("nginx mount", emptyDash(o.Path)).
		Add("systemd", activeLabel(o.SystemdActive)).
		Add("Queue", fmt.Sprintf("%d queued, %d dead-letter", o.QueueDepth, o.DeadLetters))
	plan.Render(ios.Out, ios)

	if len(o.Recent) > 0 {
		fmt.Fprintln(ios.Out)
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Recent deliveries:"))
		for _, j := range o.Recent {
			fmt.Fprintf(ios.Out, "  %s %s  %s  %s  retries=%d\n",
				j.EnqueuedAt.Format("01-02 15:04:05"),
				tui.Styles.Identifier.Render(j.Deploy),
				short(j.SHA),
				tui.Styles.Muted.Render(j.Event),
				j.Retries)
		}
	}
	return nil
}

func short(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func activeLabel(a bool) string {
	if a {
		return "active"
	}
	return "inactive"
}
