// Package dashboard is the parent of `apigw dashboard …`.
package dashboard

import (
	"github.com/spf13/cobra"

	dashOpen "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/open"
	dashServe "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/serve"
	dashStart "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/start"
	dashStatus "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/status"
	dashStop "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/stop"
	dashURL "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard/url"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

func NewCmdDashboard(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboard <command>",
		Short: "Live web UI: deploys, logs, webhooks, TLS",
		Long: "Single-process daemon: dashboard HTTP + webhook receiver + deploy\n" +
			"worker + TLS expiry ticker. All streamed live via Server-Sent Events;\n" +
			"the dashboard never needs a manual refresh.",
		Example: `  $ apigw dashboard start
  $ apigw dashboard open
  $ apigw dashboard status`,
	}
	cmd.AddCommand(dashServe.NewCmdServe(f))
	cmd.AddCommand(dashStart.NewCmdStart(f))
	cmd.AddCommand(dashStop.NewCmdStop(f))
	cmd.AddCommand(dashStatus.NewCmdStatus(f))
	cmd.AddCommand(dashURL.NewCmdURL(f))
	cmd.AddCommand(dashOpen.NewCmdOpen(f))
	return cmd
}
