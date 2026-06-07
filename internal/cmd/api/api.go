// Package api is the parent of the `apigw api …` subcommand tree.
//
// Each verb lives in its own subpackage (list/, add/, remove/, enable/,
// disable/, reload/) mirroring gh's pkg/cmd/<noun>/<verb>/ layout. The split
// per verb keeps test files small and lets each command own its own flag set.
package api

import (
	"github.com/spf13/cobra"

	apiACL "github.com/devevghenicernev-png/apigw/internal/cmd/api/acl"
	apiAdd "github.com/devevghenicernev-png/apigw/internal/cmd/api/add"
	apiBlueGreen "github.com/devevghenicernev-png/apigw/internal/cmd/api/bluegreen"
	apiCanary "github.com/devevghenicernev-png/apigw/internal/cmd/api/canary"
	apiDisable "github.com/devevghenicernev-png/apigw/internal/cmd/api/disable"
	apiEnable "github.com/devevghenicernev-png/apigw/internal/cmd/api/enable"
	apiList "github.com/devevghenicernev-png/apigw/internal/cmd/api/list"
	apiImport "github.com/devevghenicernev-png/apigw/internal/cmd/api/openapiimport"
	apiReload "github.com/devevghenicernev-png/apigw/internal/cmd/api/reload"
	apiRemove "github.com/devevghenicernev-png/apigw/internal/cmd/api/remove"
	apiRetry "github.com/devevghenicernev-png/apigw/internal/cmd/api/retry"
	apiVariants "github.com/devevghenicernev-png/apigw/internal/cmd/api/variants"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// NewCmdAPI returns the `apigw api` parent command with all verb subcommands
// attached. Adding a new verb is one import + one AddCommand below.
func NewCmdAPI(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "api <command>",
		Short:   "Manage registered upstream APIs",
		Long:    "Register, list, enable, disable, and remove the upstream services nginx routes to.",
		Aliases: []string{"apis"},
		Example: `  $ apigw api list
  $ apigw api add hello --port 8080
  $ apigw api remove hello
  $ apigw api disable hello`,
	}
	cmd.AddCommand(apiList.NewCmdList(f))
	cmd.AddCommand(apiAdd.NewCmdAdd(f))
	cmd.AddCommand(apiRemove.NewCmdRemove(f))
	cmd.AddCommand(apiEnable.NewCmdEnable(f))
	cmd.AddCommand(apiDisable.NewCmdDisable(f))
	cmd.AddCommand(apiReload.NewCmdReload(f))
	cmd.AddCommand(apiImport.NewCmdImport(f))
	cmd.AddCommand(apiACL.NewCmdACL(f))
	cmd.AddCommand(apiCanary.NewCmdCanary(f))
	cmd.AddCommand(apiBlueGreen.NewCmdBlueGreen(f))
	cmd.AddCommand(apiVariants.NewCmdVariants(f))
	cmd.AddCommand(apiRetry.NewCmdRetry(f))
	return cmd
}
