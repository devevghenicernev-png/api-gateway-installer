// Package ocsp owns `apigw tls ocsp ...` — toggle server-cert
// OCSP stapling and inspect the per-client cert cache used by
// mTLS-revocation. Per-API client-cert OCSPCheck is configured via
// `apigw api edit` / yaml; this command tree just covers the
// server-stapling toggle and a cache inspector.
package ocsp

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
)

func defaultCachePath() string {
	return filepath.Join(paths.StateDir(), "ocsp.db")
}

// NewCmdOCSP returns the `apigw tls ocsp` parent command.
func NewCmdOCSP(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ocsp <command>",
		Short: "Toggle OCSP stapling for our server cert and inspect the mTLS revocation cache",
		Long: "OCSP (RFC 6960) revocation checking comes in two flavours:\n\n" +
			"  1. Server-side stapling — nginx fetches our own cert's OCSP\n" +
			"     response and includes it in the TLS handshake so clients\n" +
			"     don't have to. Toggled by these subcommands.\n\n" +
			"  2. Client-cert checking — for mTLS APIs, the dashboard verifies\n" +
			"     each presented client cert against its issuer's OCSP\n" +
			"     responder. Enabled per-API via `apigw api edit <name>` →\n" +
			"     `mtls.ocsp_check: true` (and optionally `ocsp_soft_fail`).\n\n" +
			"Heads up: Let's Encrypt killed their OCSP responders in Aug 2025.\n" +
			"Server-stapling has no effect for LE certs. Use it with DigiCert,\n" +
			"Sectigo, or an internal CA.",
		Example: `  $ sudo apigw tls ocsp enable
  $ sudo apigw tls ocsp status
  $ sudo apigw tls ocsp disable`,
	}
	cmd.AddCommand(newEnable(f))
	cmd.AddCommand(newDisable(f))
	cmd.AddCommand(newStatus(f))
	return cmd
}

func newEnable(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Turn on ssl_stapling for our server cert and reload nginx",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			if cfg.TLS.Strategy == "" || cfg.TLS.Strategy == "none" {
				return fmt.Errorf("TLS is not configured — run `apigw tls enable …` first")
			}
			if cfg.TLS.OCSPStapling {
				fmt.Fprintln(f.IOStreams.Out, "ssl_stapling already on")
				return nil
			}
			cfg.TLS.OCSPStapling = true
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
				return fmt.Errorf("nginx reload: %w", err)
			}
			fmt.Fprintln(f.IOStreams.Out, "ssl_stapling on — nginx reloaded")
			return nil
		},
	}
}

func newDisable(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Turn off ssl_stapling and reload nginx",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			if !cfg.TLS.OCSPStapling {
				fmt.Fprintln(f.IOStreams.Out, "ssl_stapling already off")
				return nil
			}
			cfg.TLS.OCSPStapling = false
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
				return fmt.Errorf("nginx reload: %w", err)
			}
			fmt.Fprintln(f.IOStreams.Out, "ssl_stapling off — nginx reloaded")
			return nil
		},
	}
}

func newStatus(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show server-stapling config + mTLS-revocation cache stats",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			out := f.IOStreams.Out
			fmt.Fprintln(out, "server-side OCSP stapling:")
			if cfg.TLS.OCSPStapling {
				fmt.Fprintln(out, "  enabled")
			} else {
				fmt.Fprintln(out, "  disabled")
			}

			fmt.Fprintln(out)
			fmt.Fprintln(out, "per-API client-cert OCSP check:")
			anyAPI := false
			for _, a := range cfg.APIs {
				if a.MTLS == nil || !a.MTLS.OCSPCheck {
					continue
				}
				anyAPI = true
				mode := "hard-fail"
				if a.MTLS.OCSPSoftFail {
					mode = "soft-fail"
				}
				fmt.Fprintf(out, "  %s: %s (cache TTL cap: %s)\n", a.Name, mode, a.MTLS.OCSPCacheTTL)
			}
			if !anyAPI {
				fmt.Fprintln(out, "  no APIs have mtls.ocsp_check enabled")
			}

			fmt.Fprintln(out)
			fmt.Fprintln(out, "revocation cache:")
			path := defaultCachePath()
			if cfg.Security.StateDir != "" {
				path = cfg.Security.StateDir + "/ocsp.db"
			}
			fmt.Fprintf(out, "  path:   %s\n", path)
			cache, err := apitls.OpenOCSPCache(path)
			if err != nil {
				fmt.Fprintf(out, "  open:   %v\n", err)
				return nil
			}
			defer cache.Close()
			fmt.Fprintf(out, "  size:   %d entries\n", cache.Size())
			dropped, _ := cache.Purge()
			fmt.Fprintf(out, "  purged: %d stale entries\n", dropped)
			return nil
		},
	}
}
