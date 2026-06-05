// Package mtls implements `apigw auth mtls …` for configuring per-API
// mutual TLS validation.
package mtls

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdMTLS(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mtls <command>",
		Short: "Configure mutual TLS (client cert auth)",
	}
	cmd.AddCommand(newCmdSet(f))
	cmd.AddCommand(newCmdDisable(f))
	return cmd
}

func newCmdSet(f *cmdutil.Factory) *cobra.Command {
	var (
		caFile       string
		allowCNs     []string
		allowSANs    []string
		allowFprints []string
		optional     bool
	)
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Enable mTLS on an API",
		Args:  cobra.ExactArgs(1),
		Example: `  # Any cert signed by ca.pem
  $ apigw auth mtls set billing --ca /etc/apigw/ca.pem

  # Pin specific CNs (or wildcard)
  $ apigw auth mtls set billing --ca /etc/apigw/ca.pem \
      --allow-cn client.example.com --allow-cn '*.partners.example.com'

  # Cert pinning by SHA-256 fingerprint
  $ apigw auth mtls set billing --ca /etc/apigw/ca.pem \
      --allow-fingerprint deadbeef…

  # Opt-in mode (no cert = pass through with no identity)
  $ apigw auth mtls set billing --ca /etc/apigw/ca.pem --optional`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(caFile) == "" {
				return tui.NewError("--ca is required", "path to CA bundle PEM")
			}
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return tui.NewError(fmt.Sprintf("api %q not found", args[0]), "")
			}
			api.MTLS = &config.MTLS{
				CAFile:            caFile,
				AllowCNs:          allowCNs,
				AllowSANs:         allowSANs,
				AllowFingerprints: allowFprints,
				Optional:          optional,
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "✓ mTLS enabled for %q (ca=%s)\n", args[0], caFile)
			fmt.Fprintln(f.IOStreams.Out, "  Run `apigw api reload` to push the new nginx config.")
			fmt.Fprintln(f.IOStreams.Out, "  Note: mTLS requires HTTPS — make sure `apigw tls enable` ran.")
			return nil
		},
	}
	cmd.Flags().StringVar(&caFile, "ca", "", "path to CA bundle PEM (required)")
	cmd.Flags().StringSliceVar(&allowCNs, "allow-cn", nil, "allowed CN (repeatable, * wildcards)")
	cmd.Flags().StringSliceVar(&allowSANs, "allow-san", nil, "allowed SAN (repeatable)")
	cmd.Flags().StringSliceVar(&allowFprints, "allow-fingerprint", nil, "pinned SHA-256 fingerprint, hex lowercase")
	cmd.Flags().BoolVar(&optional, "optional", false, "allow requests without client cert (no identity)")
	return cmd
}

func newCmdDisable(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <api>",
		Short: "Remove mTLS from an API",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return tui.NewError(fmt.Sprintf("api %q not found", args[0]), "")
			}
			api.MTLS = nil
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "✓ mTLS removed from %q\n", args[0])
			return nil
		},
	}
}
