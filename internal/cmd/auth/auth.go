// Package auth is the parent of `apigw auth …` commands.
//
// Two flavors of auth are exposed:
//
//  1. JWT validation in-process. See `apigw auth jwt set` — wires API.JWT
//     and the dashboard daemon's /auth/jwt/<api> endpoint. nginx
//     auth_request to that endpoint validates every request.
//
//  2. OIDC via oauth2-proxy. See `apigw auth oidc setup` — interactive
//     wizard that generates an oauth2-proxy.cfg + wires forward_auth on
//     the chosen APIs to point at oauth2-proxy. Operators install the
//     oauth2-proxy binary separately (apt / brew / scratch container).
package auth

import (
	"github.com/spf13/cobra"

	admincmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/admin"
	apikeycmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/apikey"
	hmaccmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/hmac"
	jwtcmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/jwt"
	mtlscmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/mtls"
	oidccmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth/oidc"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

func NewCmdAuth(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <command>",
		Short: "JWT / OIDC authentication for APIs",
		Long: "Two paths:\n" +
			"  1) `auth jwt set` — in-process JWT validation via dashboard daemon\n" +
			"  2) `auth oidc setup` — OIDC via oauth2-proxy + forward_auth wizard",
		Example: `  $ apigw auth jwt set billing --algorithm RS256 --jwks-url https://auth.example.com/.well-known/jwks.json
  $ apigw auth oidc setup`,
	}
	cmd.AddCommand(jwtcmd.NewCmdJWT(f))
	cmd.AddCommand(oidccmd.NewCmdOIDC(f))
	cmd.AddCommand(mtlscmd.NewCmdMTLS(f))
	cmd.AddCommand(admincmd.NewCmdAdmin(f))
	cmd.AddCommand(apikeycmd.NewCmdAPIKey(f))
	cmd.AddCommand(hmaccmd.NewCmdHMAC(f))
	return cmd
}
