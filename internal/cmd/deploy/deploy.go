// Package deploy is the parent of `apigw deploy …`.
package deploy

import (
	"github.com/spf13/cobra"

	deployAdd "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/add"
	deployList "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/list"
	deployLogs "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/logs"
	deployRemove "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/remove"
	deployRun "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/run"
	deploySSHKey "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/sshkey"
	deployStatus "github.com/devevghenicernev-png/apigw/internal/cmd/deploy/status"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

func NewCmdDeploy(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deploy <command>",
		Short:   "Clone, build, and run git-backed services",
		Aliases: []string{"deployments"},
		Example: `  $ apigw deploy add hello --repo https://github.com/owner/hello --port 3000
  $ apigw deploy list
  $ apigw deploy run hello
  $ apigw deploy logs hello --follow
  $ apigw deploy ssh-key            # show or generate the deploy key`,
	}
	cmd.AddCommand(deployAdd.NewCmdAdd(f))
	cmd.AddCommand(deployList.NewCmdList(f))
	cmd.AddCommand(deployRemove.NewCmdRemove(f))
	cmd.AddCommand(deployRun.NewCmdRun(f))
	cmd.AddCommand(deployStatus.NewCmdStatus(f))
	cmd.AddCommand(deployLogs.NewCmdLogs(f))
	cmd.AddCommand(deploySSHKey.NewCmdSSHKey(f))
	return cmd
}
