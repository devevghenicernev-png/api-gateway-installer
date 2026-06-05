// Package webhook is the parent of `apigw webhook …`.
package webhook

import (
	whList "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/list"
	whRotate "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/rotate"
	whServe "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/serve"
	whSetup "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/setup"
	whStart "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/start"
	whStatus "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/status"
	whStop "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/stop"
	whURL "github.com/devevghenicernev-png/apigw/internal/cmd/webhook/url"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdWebhook(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook <command>",
		Short: "GitHub webhook receiver + deploy queue",
		Long: "Receives push events from GitHub and triggers deploys.\n\n" +
			"Each deploy gets its own webhook URL and HMAC secret. The server\n" +
			"validates signatures in constant time and persists jobs to a local\n" +
			"bbolt queue so a restart never loses work.",
		Example: `  $ apigw webhook start
  $ apigw webhook setup hello
  $ apigw webhook list
  $ apigw webhook status
  $ apigw webhook rotate-secret hello`,
	}
	cmd.AddCommand(whServe.NewCmdServe(f))
	cmd.AddCommand(whStart.NewCmdStart(f))
	cmd.AddCommand(whStop.NewCmdStop(f))
	cmd.AddCommand(whStatus.NewCmdStatus(f))
	cmd.AddCommand(whList.NewCmdList(f))
	cmd.AddCommand(whURL.NewCmdURL(f))
	cmd.AddCommand(whSetup.NewCmdSetup(f))
	cmd.AddCommand(whRotate.NewCmdRotate(f))
	return cmd
}
