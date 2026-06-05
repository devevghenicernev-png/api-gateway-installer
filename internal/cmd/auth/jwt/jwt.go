// Package jwt configures per-API JWT validation. The actual verifier lives
// in internal/auth/jwt.go and runs in the dashboard daemon.
package jwt

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdJWT(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jwt <command>",
		Short: "Configure JWT validation on an API",
	}
	cmd.AddCommand(newCmdSet(f))
	cmd.AddCommand(newCmdDisable(f))
	return cmd
}

func newCmdSet(f *cmdutil.Factory) *cobra.Command {
	var (
		algo       string
		hmacSecret string
		jwksURL    string
		issuer     string
		audience   string
	)
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Enable JWT validation for an API",
		Args:  cobra.ExactArgs(1),
		Example: `  # HMAC-shared-secret (symmetric)
  $ apigw auth jwt set billing --algorithm HS256 --hmac-secret "$JWT_SECRET"

  # JWKS-based (Auth0, Keycloak, Okta, custom OIDC)
  $ apigw auth jwt set billing --algorithm RS256 \
      --jwks-url https://auth.example.com/.well-known/jwks.json \
      --issuer https://auth.example.com/ --audience billing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			name := args[0]
			api := cfg.FindAPI(name)
			if api == nil {
				return tui.NewError(fmt.Sprintf("api %q not found", name),
					"register it first with `apigw api add`")
			}
			if algo == "" {
				return tui.NewError("--algorithm is required",
					"valid: HS256/HS384/HS512/RS256/RS384/RS512/ES256/ES384/ES512/EdDSA")
			}
			if hmacSecret == "" && jwksURL == "" {
				return tui.NewError("either --hmac-secret or --jwks-url is required",
					"HS* algorithms use --hmac-secret; RS*/ES*/EdDSA use --jwks-url")
			}
			api.JWT = &config.JWT{
				Algorithm:  algo,
				HMACSecret: hmacSecret,
				JWKSURL:    jwksURL,
				Issuer:     issuer,
				Audience:   audience,
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "✓ JWT validation enabled for %q\n", name)
			fmt.Fprintln(f.IOStreams.Out, "  Run `apigw api reload` to push the new nginx config.")
			return nil
		},
	}
	cmd.Flags().StringVar(&algo, "algorithm", "", "HS256/HS384/HS512/RS256/RS384/RS512/ES256/ES384/ES512/EdDSA")
	cmd.Flags().StringVar(&hmacSecret, "hmac-secret", "", "shared secret for HS* algorithms")
	cmd.Flags().StringVar(&jwksURL, "jwks-url", "", "JWKS endpoint for RS*/ES*/EdDSA")
	cmd.Flags().StringVar(&issuer, "issuer", "", "required iss claim (optional)")
	cmd.Flags().StringVar(&audience, "audience", "", "required aud claim (optional)")
	return cmd
}

func newCmdDisable(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <api>",
		Short: "Remove JWT validation from an API",
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
			api.JWT = nil
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "✓ JWT validation removed from %q\n", args[0])
			return nil
		},
	}
}
