// Package status implements `apigw status` — a consolidated health overview
// across nginx, TLS, deploys, ports, and the webhook queue.
//
// `apigw status` is the operator's "is everything fine?" command. `apigw
// doctor` is the deeper dive. Status mirrors `kubectl get pods` in shape:
// a few sections, terse one-liners, color-coded states.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show a one-shot health overview",
		Args:  cobra.NoArgs,
		Example: `  $ apigw status
  $ apigw status --json | jq
  $ apigw status --watch       # re-render in place when state changes`,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			return run(c.Context(), f, asJSON, watch)
		},
	}
	cmd.Flags().BoolVarP(&watch, "watch", "w", false,
		"re-render in place every 2s (Ctrl-C to exit)")
	return cmd
}

// Snapshot is what we emit for --json. Stable schema — never change a field
// without bumping the binary's --version major.
type Snapshot struct {
	Generated time.Time         `json:"generated"`
	System    SystemSummary     `json:"system"`
	Services  []ServiceStatus   `json:"services"`
	TLS       []apitls.CertInfo `json:"tls"`
	Deploys   []DeployStatus    `json:"deploys"`
	Webhook   WebhookSummary    `json:"webhook"`
}

type SystemSummary struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type ServiceStatus struct {
	Unit   string `json:"unit"`
	Active bool   `json:"active"`
}

type DeployStatus struct {
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Port       int    `json:"port"`
	LastSHA    string `json:"last_sha"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error,omitempty"`
}

type WebhookSummary struct {
	Enabled    bool `json:"enabled"`
	Port       int  `json:"port"`
	QueueDepth int  `json:"queue_depth"`
	DeadLetter int  `json:"dead_letter"`
}

func run(ctx context.Context, f *cmdutil.Factory, asJSON, watch bool) error {
	if !watch {
		return one(ctx, f, asJSON)
	}
	return loop(ctx, f, asJSON)
}

func one(ctx context.Context, f *cmdutil.Factory, asJSON bool) error {
	snap, err := build(ctx, f)
	if err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}
	render(f, snap)
	return nil
}

// loop re-snapshots every 2s and rewrites the screen using cursor
// positioning, never clear-screen — the design system rule from
// ARCHITECTURE.md §"CLI design system" #12.
func loop(ctx context.Context, f *cmdutil.Factory, asJSON bool) error {
	if asJSON {
		// --json + --watch is line-delimited NDJSON, one snapshot per line.
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			snap, err := build(ctx, f)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(snap)
			fmt.Fprintln(f.IOStreams.Out, string(b))
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
		}
	}

	// Interactive: track lines written, return cursor with ANSI escapes.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	written := 0
	for {
		// Cursor up `written` lines + clear-to-end-of-screen.
		if written > 0 {
			fmt.Fprintf(f.IOStreams.Out, "\x1b[%dA\x1b[J", written)
		}
		snap, err := build(ctx, f)
		if err != nil {
			return err
		}
		written = renderCounted(f, snap)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func build(ctx context.Context, f *cmdutil.Factory) (Snapshot, error) {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Generated: time.Now().UTC(),
		System:    SystemSummary{OS: runtime.GOOS, Arch: runtime.GOARCH},
	}

	// Services we own: nginx + apigw-{dashboard,webhook} + tls-renew.timer.
	for _, unit := range []string{
		"nginx.service",
		"apigw-dashboard.service",
		"apigw-webhook.service",
		"apigw-tls-renew.timer",
	} {
		snap.Services = append(snap.Services, ServiceStatus{
			Unit:   unit,
			Active: isActive(ctx, unit),
		})
	}

	// TLS
	if certs, err := apitls.ListCerts(); err == nil {
		snap.TLS = certs
	}

	// Deploys
	for _, d := range cfg.Deploys {
		snap.Deploys = append(snap.Deploys, DeployStatus{
			Name:       d.Name,
			Active:     deploy.IsActive(d.Name),
			Port:       d.Port,
			LastSHA:    d.LastSHA,
			LastStatus: d.LastStatus,
			LastError:  d.LastError,
		})
	}

	// Webhook queue
	snap.Webhook = WebhookSummary{Enabled: cfg.Webhook.Enabled, Port: cfg.Webhook.Port}
	if q, err := webhook.OpenQueue(); err == nil {
		snap.Webhook.QueueDepth, snap.Webhook.DeadLetter, _ = q.Depth()
		_ = q.Close()
	}

	return snap, nil
}

func isActive(ctx context.Context, unit string) bool {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit)
	return cmd.Run() == nil
}

func render(f *cmdutil.Factory, s Snapshot) {
	_ = renderCounted(f, s)
}

// renderCounted draws the snapshot and returns the number of lines written.
// The --watch loop uses the count to position the cursor before the next
// repaint without a clear-screen.
func renderCounted(f *cmdutil.Factory, s Snapshot) int {
	ios := f.IOStreams
	n := 0

	header := fmt.Sprintf("apigw status — %s",
		s.Generated.Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintln(ios.Out, tui.Styles.Heading.Render(header))
	n++

	// Services
	fmt.Fprintln(ios.Out)
	n++
	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Services"))
	n++
	for _, svc := range s.Services {
		mark := tui.Styles.Danger.Render(tui.GlyphCross)
		state := "inactive"
		if svc.Active {
			mark = tui.Styles.Success.Render(tui.GlyphCheck)
			state = "active"
		}
		fmt.Fprintf(ios.Out, "  %s %s  %s\n", mark,
			tui.Styles.Identifier.Render(svc.Unit),
			tui.Styles.Muted.Render(state))
		n++
	}

	// TLS
	if len(s.TLS) > 0 {
		fmt.Fprintln(ios.Out)
		n++
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("TLS"))
		n++
		for _, c := range s.TLS {
			mark, state := tui.Styles.Success.Render(tui.GlyphCheck), tui.Styles.Success.Render(fmt.Sprintf("%dd left", c.DaysLeft))
			if c.DaysLeft < 0 {
				mark, state = tui.Styles.Danger.Render(tui.GlyphCross), tui.Styles.Danger.Render(fmt.Sprintf("expired %dd ago", -c.DaysLeft))
			} else if c.DaysLeft < 30 {
				mark, state = tui.Styles.Warn.Render("!"), tui.Styles.Warn.Render(fmt.Sprintf("%dd left", c.DaysLeft))
			}
			fmt.Fprintf(ios.Out, "  %s %s  %s  %s\n",
				mark,
				tui.Styles.Identifier.Render(c.Domain),
				state,
				tui.Styles.Muted.Render(string(c.Strategy)))
			n++
		}
	}

	// Deploys
	if len(s.Deploys) > 0 {
		fmt.Fprintln(ios.Out)
		n++
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Deploys"))
		n++
		for _, d := range s.Deploys {
			mark := tui.Styles.Muted.Render("·")
			switch {
			case d.LastStatus == "failed":
				mark = tui.Styles.Danger.Render(tui.GlyphCross)
			case d.Active:
				mark = tui.Styles.Success.Render(tui.GlyphCheck)
			}
			sha := d.LastSHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			fmt.Fprintf(ios.Out, "  %s %s  %s  %s\n",
				mark,
				tui.Styles.Identifier.Render(d.Name),
				tui.Styles.Muted.Render(sha),
				tui.Styles.Muted.Render(d.LastStatus))
			n++
		}
	}

	// Webhook
	fmt.Fprintln(ios.Out)
	n++
	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Webhook"))
	n++
	state := tui.Styles.Muted.Render("disabled")
	if s.Webhook.Enabled {
		state = tui.Styles.Success.Render(fmt.Sprintf("on :%d", s.Webhook.Port))
	}
	fmt.Fprintf(ios.Out, "  %s  queue=%d  dead=%d\n",
		state, s.Webhook.QueueDepth, s.Webhook.DeadLetter)
	n++

	return n
}
