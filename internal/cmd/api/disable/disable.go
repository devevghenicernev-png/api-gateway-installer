// Package disable implements `apigw api disable`. The flow mirrors enable
// exactly — it just passes the opposite boolean — so we reuse enable.Run.
package disable

import (
	"github.com/devevghenicernev-png/apigw/internal/cmd/api/enable"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdDisable(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable an upstream API without removing its config",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return enable.Run(f, args[0], false)
		},
	}
}
