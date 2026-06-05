// Package tls is the parent of `apigw tls …`. Verbs live in subpackages.
package tls

import (
	"github.com/spf13/cobra"

	tlsAddDomain "github.com/devevghenicernev-png/apigw/internal/cmd/tls/adddomain"
	tlsDisable "github.com/devevghenicernev-png/apigw/internal/cmd/tls/disable"
	tlsEnable "github.com/devevghenicernev-png/apigw/internal/cmd/tls/enable"
	tlsRenew "github.com/devevghenicernev-png/apigw/internal/cmd/tls/renew"
	tlsStatus "github.com/devevghenicernev-png/apigw/internal/cmd/tls/status"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// NewCmdTLS returns the `apigw tls` parent command.
func NewCmdTLS(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tls <command>",
		Short: "Manage HTTPS — Let's Encrypt, DuckDNS, or self-signed",
		Long: "Set up, renew, inspect, and remove HTTPS for apigw.\n\n" +
			"Three strategies: letsencrypt (public domain + HTTP-01), duckdns " +
			"(free *.duckdns.org via DNS-01), self-signed (dev/local only).",
		Example: `  $ apigw tls enable letsencrypt --domain api.example.com --email me@example.com
  $ apigw tls enable duckdns --subdomain orangepiapi --token <tok> --email me@example.com
  $ apigw tls enable self-signed --cn orangepi.local
  $ apigw tls status
  $ apigw tls renew --dry-run`,
	}
	cmd.AddCommand(tlsEnable.NewCmdEnable(f))
	cmd.AddCommand(tlsAddDomain.NewCmdAddDomain(f))
	cmd.AddCommand(tlsStatus.NewCmdStatus(f))
	cmd.AddCommand(tlsRenew.NewCmdRenew(f))
	cmd.AddCommand(tlsDisable.NewCmdDisable(f))
	return cmd
}
