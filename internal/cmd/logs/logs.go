// Package logs implements `apigw logs` — a unified log stream across nginx,
// the webhook server, the dashboard, and (optionally) a specific deploy.
//
// Sources:
//
//	--service nginx              tail /var/log/nginx/access.log + error.log
//	--service webhook            journalctl -u apigw-webhook.service
//	--service dashboard          journalctl -u apigw-dashboard.service
//	--service deploy:<name>      same as `apigw deploy logs <name>`
//	(no flag)                    interleave webhook + dashboard + nginx errors
//
// Phase 5.1 will wire nginx access logs into the events.Hub so this
// command can be a single SSE subscriber. v1 shells out to journalctl /
// tail; correct and simple, just not the prettiest single transport.
package logs

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	Service string
	Follow  bool
	Lines   int
}

func NewCmdLogs(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream logs from nginx, webhook, dashboard, or a deploy",
		Args:  cobra.NoArgs,
		Example: `  $ apigw logs --service nginx --follow
  $ apigw logs --service webhook -n 500
  $ apigw logs --service deploy:hello --follow`,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Service, "service", "",
		"source: nginx | webhook | dashboard | deploy:<name>")
	cmd.Flags().BoolVarP(&opts.Follow, "follow", "f", false, "stream new lines as they arrive")
	cmd.Flags().IntVarP(&opts.Lines, "lines", "n", 100, "number of historical lines to show")
	return cmd
}

func run(opts *options) error {
	svc := strings.ToLower(strings.TrimSpace(opts.Service))
	if svc == "" {
		// No filter — pipe journalctl for everything apigw-related plus nginx.
		return runJournalCtl(opts, "--unit=apigw-dashboard.service",
			"--unit=apigw-webhook.service",
			"--unit=apigw-tls-renew.service",
			"--unit=nginx.service")
	}
	if strings.HasPrefix(svc, "deploy:") {
		name := strings.TrimPrefix(svc, "deploy:")
		if name == "" {
			return tui.NewError("invalid --service", "deploy: requires a name").
				WithFix("apigw logs --service deploy:hello", "specify a deploy").
				WithDocs("E_LOGS_BAD_SERVICE")
		}
		return runJournalCtl(opts, "-u", "apigw-deploy@"+name+".service")
	}
	switch svc {
	case "nginx":
		return tailNginx(opts)
	case "webhook":
		return runJournalCtl(opts, "-u", "apigw-webhook.service")
	case "dashboard":
		return runJournalCtl(opts, "-u", "apigw-dashboard.service")
	default:
		return tui.NewError("unknown --service", fmt.Sprintf("got %q", svc)).
			WithFix("apigw logs --service webhook", "supported: nginx, webhook, dashboard, deploy:<name>").
			WithDocs("E_LOGS_BAD_SERVICE")
	}
}

func runJournalCtl(opts *options, prefix ...string) error {
	args := append([]string{}, prefix...)
	args = append(args, "--lines", strconv.Itoa(opts.Lines))
	if opts.Follow {
		args = append(args, "--follow")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = opts.f.IOStreams.Out
	cmd.Stderr = opts.f.IOStreams.ErrOut
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

// tailNginx tails /var/log/nginx/access.log + error.log. We fan them into
// one stdout via `tail -F` which handles logrotate cleanly.
func tailNginx(opts *options) error {
	access := "/var/log/nginx/access.log"
	errlog := "/var/log/nginx/error.log"

	args := []string{"-n", strconv.Itoa(opts.Lines)}
	if opts.Follow {
		args = []string{"-F", "-n", strconv.Itoa(opts.Lines)}
	}
	args = append(args, access, errlog)
	cmd := exec.Command("tail", args...)
	cmd.Stdout = opts.f.IOStreams.Out
	cmd.Stderr = opts.f.IOStreams.ErrOut
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return cmdutil.SilentError
		}
		return tui.NewError("tail nginx logs failed", err.Error()).
			WithFix("ls /var/log/nginx/", "verify the files exist").
			WithDocs("E_LOGS_NGINX")
	}
	return nil
}
