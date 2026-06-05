// Package logs implements `apigw deploy logs <name>`.
//
// Two transports:
//
//   - Live (--follow + local dashboard reachable): subscribes to
//     /events?topic=deploy.<name>.stdout via the in-process event hub. This
//     is the sub-100 ms path documented in DESIGN.md §5c.
//   - Historical (--lines N or no --follow): journalctl shell-out. Phase 5.1
//     will plumb the bbolt ring buffer here too.
//
// Fallback: if the dashboard isn't running, --follow degrades to
// `journalctl -f`. Operators get useful output either way.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	apideploy "github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/events"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdLogs(f *cmdutil.Factory) *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show deployment logs (live via SSE when dashboard is running)",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw deploy logs hello
  $ apigw deploy logs hello --follow
  $ apigw deploy logs hello --lines 500`,
		RunE: func(c *cobra.Command, args []string) error {
			return run(c.Context(), f, args[0], follow, lines)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new lines as they arrive")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "number of historical lines to show")
	return cmd
}

func run(ctx context.Context, f *cmdutil.Factory, name string, follow bool, lines int) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if cfg.FindDeploy(name) == nil {
		return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
			WithDocs("E_DEPLOY_NOT_FOUND")
	}

	dashboardPort := cfg.Dashboard.Port
	if dashboardPort == 0 {
		dashboardPort = 9080
	}

	if follow && dashboardReachable(dashboardPort) {
		return followViaSSE(ctx, f, name, dashboardPort)
	}
	return tailViaJournal(f, name, follow, lines)
}

// followViaSSE streams build/stdout lines from the running dashboard.
//
// This is the <100 ms latency path: lines hit the events.Hub straight from
// the build subprocess's StdoutPipe, get fanned to our SSE handler, and
// arrive here as JSON-encoded payloads we unwrap and print.
func followViaSSE(ctx context.Context, f *cmdutil.Factory, name string, port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/events?topic=deploy.%s.stdout", port, name)
	c := events.NewClient(url)
	out := f.IOStreams.Out
	c.OnEvent = func(e events.Event) {
		if e.Type != "stdout" {
			return
		}
		var payload struct {
			Line   string `json:"line"`
			Stream string `json:"stream"`
		}
		if err := json.Unmarshal(e.Data, &payload); err != nil {
			return
		}
		fmt.Fprintln(out, payload.Line)
	}
	return c.Run(ctx)
}

// tailViaJournal falls back to journalctl. Same flow as Phase 3's
// implementation — kept here so removing the dashboard doesn't break logs.
func tailViaJournal(f *cmdutil.Factory, name string, follow bool, lines int) error {
	args := append([]string{}, apideploy.JournalCmd(name)...)
	args = append(args, "--lines", strconv.Itoa(lines))
	if follow {
		args = append(args, "--follow")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = f.IOStreams.Out
	cmd.Stderr = f.IOStreams.ErrOut
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return cmdutil.SilentError
		}
		return tui.NewError("journalctl failed", err.Error()).
			WithFix("which journalctl", "verify systemd is installed").
			WithDocs("E_JOURNAL")
	}
	return nil
}

func dashboardReachable(port int) bool {
	d := net.Dialer{Timeout: 250 * time.Millisecond}
	c, err := d.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
