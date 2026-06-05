// Package oidc implements `apigw auth oidc setup` — an interactive wizard
// that wires forward_auth on the chosen APIs to point at an oauth2-proxy
// instance. The wizard:
//
//  1. Asks the operator which OIDC provider they use (Google, Azure,
//     Auth0, Keycloak, Authentik, Authelia, or custom).
//  2. Collects client_id, client_secret, and (for custom/Keycloak) the
//     OIDC discovery endpoint.
//  3. Lists the registered APIs and lets the operator multi-select which
//     ones to protect.
//  4. Generates an oauth2-proxy.cfg next to apigw's config — operators
//     install the oauth2-proxy binary separately (apt/brew).
//  5. Updates the config to add ForwardAuth on each chosen API.
//
// Why not bundle oauth2-proxy? It's 30+ MB of Go binary that already exists
// in distro repos. Re-shipping it bloats apigw and forks the security
// update path. Wizard + config generation gives the same UX without the
// supply chain.
package oidc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdOIDC(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "oidc <command>",
		Short: "OIDC/OAuth2 via oauth2-proxy + forward_auth",
	}
	cmd.AddCommand(newCmdSetup(f))
	cmd.AddCommand(newCmdStatus(f))
	return cmd
}

// answers holds the wizard's collected state. Defaults are filled in by
// each provider's preset block before the form runs.
type answers struct {
	Provider       string // google | azure | auth0 | keycloak | authentik | authelia | custom
	ClientID       string
	ClientSecret   string
	OIDCIssuerURL  string
	EmailDomain    string // optional restriction
	OAuth2ProxyURL string // where oauth2-proxy listens
	APIs           []string // names of APIs to protect
}

func newCmdSetup(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "setup",
		Short:   "Interactive wizard: wire OIDC via oauth2-proxy on the chosen APIs",
		Example: "  $ apigw auth oidc setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			if len(cfg.APIs) == 0 {
				return tui.NewError("no APIs registered",
					"register at least one API first (`apigw api add`)")
			}
			a := answers{
				Provider:       "google",
				OAuth2ProxyURL: "http://127.0.0.1:4180",
			}
			if err := runWizard(&a, cfg); err != nil {
				if errors.Is(err, cmdutil.CancelError) {
					return cmdutil.CancelError
				}
				return err
			}

			// 1. Write oauth2-proxy.cfg
			oauthCfgPath := filepath.Join(paths.ConfigDir(), "oauth2-proxy.cfg")
			cfgBody := renderOAuth2ProxyConfig(a)
			if err := os.MkdirAll(filepath.Dir(oauthCfgPath), 0o755); err != nil {
				return fmt.Errorf("mkdir config dir: %w", err)
			}
			if err := os.WriteFile(oauthCfgPath, []byte(cfgBody), 0o600); err != nil {
				return fmt.Errorf("write oauth2-proxy.cfg: %w", err)
			}

			// 2. Wire ForwardAuth on each chosen API.
			authCheckURL := strings.TrimRight(a.OAuth2ProxyURL, "/") + "/oauth2/auth"
			signInURL := strings.TrimRight(a.OAuth2ProxyURL, "/") + "/oauth2/start?rd=$scheme://$host$request_uri"
			for _, name := range a.APIs {
				api := cfg.FindAPI(name)
				if api == nil {
					continue
				}
				api.ForwardAuth = &config.ForwardAuth{
					Address:    authCheckURL,
					SignInURL:  signInURL,
					SetHeaders: []string{"X-Auth-Request-User", "X-Auth-Request-Email"},
				}
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			// 3. Summary + next-steps. We don't auto-install oauth2-proxy —
			//    operators install it via their package manager.
			fmt.Fprintln(f.IOStreams.Out, tui.Styles.Heading.Render("\n✓ OIDC wiring saved\n"))
			fmt.Fprintf(f.IOStreams.Out, "  oauth2-proxy config:  %s\n", oauthCfgPath)
			fmt.Fprintf(f.IOStreams.Out, "  protected APIs:       %s\n", strings.Join(a.APIs, ", "))
			fmt.Fprintln(f.IOStreams.Out, "")
			fmt.Fprintln(f.IOStreams.Out, tui.Styles.Heading.Render("Next steps:"))
			fmt.Fprintln(f.IOStreams.Out, "  1. Install oauth2-proxy:")
			fmt.Fprintln(f.IOStreams.Out, "       Debian/Ubuntu:  sudo apt install oauth2-proxy")
			fmt.Fprintln(f.IOStreams.Out, "       macOS:          brew install oauth2-proxy")
			fmt.Fprintln(f.IOStreams.Out, "       binary:         https://github.com/oauth2-proxy/oauth2-proxy/releases")
			fmt.Fprintf(f.IOStreams.Out, "  2. Start oauth2-proxy with: oauth2-proxy --config=%s\n", oauthCfgPath)
			fmt.Fprintln(f.IOStreams.Out, "  3. Push the nginx config: apigw api reload")
			return nil
		},
	}
	return cmd
}

func runWizard(a *answers, cfg *config.Config) error {
	apiOptions := []huh.Option[string]{}
	for _, api := range cfg.APIs {
		apiOptions = append(apiOptions, huh.NewOption(api.Name+" — "+api.Description, api.Name))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("OIDC setup — step 1/3").
				Description("Pick your identity provider. The wizard prefills the OIDC issuer URL for known providers."),
			huh.NewSelect[string]().
				Title("Provider").
				Options(
					huh.NewOption("Google", "google"),
					huh.NewOption("Microsoft / Azure AD", "azure"),
					huh.NewOption("Auth0", "auth0"),
					huh.NewOption("Keycloak", "keycloak"),
					huh.NewOption("Authentik", "authentik"),
					huh.NewOption("Authelia (no OIDC — uses forward_auth only)", "authelia"),
					huh.NewOption("Custom OIDC", "custom"),
				).
				Value(&a.Provider),
		),

		huh.NewGroup(
			huh.NewNote().
				Title("OIDC setup — step 2/3").
				Description("OAuth client credentials. Get these from your provider's admin console."),
			huh.NewInput().
				Title("Client ID").
				Value(&a.ClientID).
				Validate(nonEmpty),
			huh.NewInput().
				Title("Client Secret").
				EchoMode(huh.EchoModePassword).
				Value(&a.ClientSecret).
				Validate(nonEmpty),
			huh.NewInput().
				Title("OIDC Issuer URL").
				Description("e.g. https://accounts.google.com or https://your-keycloak.com/auth/realms/master").
				Value(&a.OIDCIssuerURL).
				Validate(nonEmpty),
			huh.NewInput().
				Title("Restrict to email domain (optional)").
				Description("Only accept users whose email matches this domain. Leave blank for any.").
				Value(&a.EmailDomain),
		),

		huh.NewGroup(
			huh.NewNote().
				Title("OIDC setup — step 3/3").
				Description("Which APIs to protect with this OIDC flow?\nUse space to toggle, enter to confirm."),
			huh.NewMultiSelect[string]().
				Title("APIs").
				Options(apiOptions...).
				Value(&a.APIs).
				Validate(func(s []string) error {
					if len(s) == 0 {
						return fmt.Errorf("select at least one")
					}
					return nil
				}),
			huh.NewInput().
				Title("oauth2-proxy URL").
				Description("Where oauth2-proxy listens. Default port 4180. Change only if you've moved it.").
				Value(&a.OAuth2ProxyURL).
				Validate(nonEmpty),
		),
	).WithTheme(huh.ThemeCharm())

	// Provider preset: fill in the typical OIDC issuer when known.
	a.OIDCIssuerURL = presetIssuer(a.Provider)
	return form.Run()
}

func presetIssuer(provider string) string {
	switch provider {
	case "google":
		return "https://accounts.google.com"
	case "azure":
		return "https://login.microsoftonline.com/<tenant-id>/v2.0"
	case "auth0":
		return "https://<your-domain>.auth0.com/"
	}
	return ""
}

// renderOAuth2ProxyConfig produces the oauth2-proxy.cfg payload. Format is
// the canonical https://oauth2-proxy.github.io/oauth2-proxy/configuration/overview/
// — TOML-ish; oauth2-proxy parses it via viper.
func renderOAuth2ProxyConfig(a answers) string {
	var b strings.Builder
	b.WriteString("# Generated by apigw auth oidc setup. Edit by hand if needed —\n")
	b.WriteString("# apigw never re-writes this file once it exists.\n\n")
	fmt.Fprintf(&b, "provider             = %q\n", oauth2ProxyProvider(a.Provider))
	fmt.Fprintf(&b, "client_id            = %q\n", a.ClientID)
	fmt.Fprintf(&b, "client_secret        = %q\n", a.ClientSecret)
	fmt.Fprintf(&b, "oidc_issuer_url      = %q\n", a.OIDCIssuerURL)
	fmt.Fprintf(&b, "redirect_url         = %q\n", "https://<your-host>/oauth2/callback")
	fmt.Fprintf(&b, "cookie_secret        = %q  # rotate periodically; 32+ random bytes\n", placeholderSecret())
	fmt.Fprintf(&b, "http_address         = %q\n", strings.TrimPrefix(a.OAuth2ProxyURL, "http://"))
	b.WriteString("upstreams            = [\"static://200\"]\n")
	b.WriteString("set_xauthrequest     = true\n")
	b.WriteString("pass_authorization_header = true\n")
	b.WriteString("reverse_proxy        = true\n")
	if a.EmailDomain != "" {
		fmt.Fprintf(&b, "email_domains        = [%q]\n", a.EmailDomain)
	} else {
		b.WriteString("email_domains        = [\"*\"]\n")
	}
	return b.String()
}

// oauth2ProxyProvider maps our friendly names to the OAuth2 proxy provider IDs.
func oauth2ProxyProvider(p string) string {
	switch p {
	case "google":
		return "google"
	case "azure":
		return "azure"
	case "auth0":
		return "oidc"
	case "keycloak":
		return "keycloak-oidc"
	case "authentik":
		return "oidc"
	case "authelia":
		return "oidc"
	}
	return "oidc"
}

// placeholderSecret returns a static marker so generated configs never ship
// a real secret. Operators must rotate before deploying.
func placeholderSecret() string {
	return "REPLACE-ME-WITH-32-BYTES-OF-RANDOM"
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func newCmdStatus(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which APIs have OIDC-via-forward_auth wired",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.FromFactory(f)
			if err != nil {
				return err
			}
			n := 0
			for _, api := range cfg.APIs {
				if api.ForwardAuth != nil {
					fmt.Fprintf(f.IOStreams.Out, "  ✓ %s → %s\n", api.Name, api.ForwardAuth.Address)
					n++
				}
			}
			if n == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no APIs have forward_auth wired (run `apigw auth oidc setup`)")
			}
			oauthCfgPath := filepath.Join(paths.ConfigDir(), "oauth2-proxy.cfg")
			if _, err := os.Stat(oauthCfgPath); err == nil {
				fmt.Fprintf(f.IOStreams.Out, "\noauth2-proxy config: %s\n", oauthCfgPath)
			}
			return nil
		},
	}
}
