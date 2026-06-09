// Package setup implements `apigw webhook setup <deploy>`.
//
// Interactive walkthrough that generates a secret and tells the user exactly
// what to paste into GitHub's UI. Same shape as `gh repo create` — guided
// but explicit. The secret is printed ONCE; we never echo it again.
package setup

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

func NewCmdSetup(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup <deploy>",
		Short: "Generate a secret and walk through GitHub's webhook setup",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rotate, _ := c.Flags().GetBool("rotate")
			return run(f, args[0], rotate)
		},
	}
	cmd.Flags().Bool("rotate", false, "regenerate the secret even if one exists")
	return cmd
}

func run(f *cmdutil.Factory, name string, rotate bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	d := cfg.FindDeploy(name)
	if d == nil {
		return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
			WithDocs("E_DEPLOY_NOT_FOUND")
	}

	var secret string
	if rotate {
		secret, err = webhook.RotateSecret(name)
	} else {
		secret, err = webhook.EnsureSecret(name)
	}
	if err != nil {
		return fmt.Errorf("manage secret: %w", err)
	}

	host := publicHost(cfg)
	https := cfg.TLS.Strategy != "" && cfg.TLS.Strategy != "none"
	url := webhook.PublicURL(host, name, https)
	contentType := "application/json"

	ios := f.IOStreams
	plan := tui.NewPlan("Webhook setup — "+name).
		Add("Payload URL", url).
		Add("Content type", contentType).
		Add("Events", "push (default)").
		Add("SSL verify", boolLabel(https, "yes", "no — set Insecure SSL=ON in GitHub UI"))
	plan.Render(ios.Out, ios)

	fmt.Fprintln(ios.Out)
	fmt.Fprintln(ios.Out, tui.Styles.Heading.Render("Secret (printed once — copy it now):"))
	fmt.Fprintln(ios.Out, "  "+tui.Styles.Identifier.Render(secret))
	fmt.Fprintln(ios.Out)

	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("In GitHub:"))
	fmt.Fprintf(ios.Out, "  %s open %s\n",
		tui.Styles.Accent.Render("1."),
		tui.Styles.URL.Render(repoSettingsURL(d.Repo)))
	fmt.Fprintf(ios.Out, "  %s click %s\n",
		tui.Styles.Accent.Render("2."),
		tui.Styles.Identifier.Render("Add webhook"))
	fmt.Fprintf(ios.Out, "  %s paste Payload URL and Secret from above\n",
		tui.Styles.Accent.Render("3."))
	fmt.Fprintf(ios.Out, "  %s set Content type to %s\n",
		tui.Styles.Accent.Render("4."),
		tui.Styles.Identifier.Render(contentType))
	fmt.Fprintf(ios.Out, "  %s click %s — GitHub will send a %s event to verify\n",
		tui.Styles.Accent.Render("5."),
		tui.Styles.Identifier.Render("Add webhook"),
		tui.Styles.Identifier.Render("ping"))
	fmt.Fprintln(ios.Out)
	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("Verify with `apigw webhook status` — Recent deliveries will show the ping."))
	return nil
}

// publicHost picks the best hostname to print in the GitHub webhook setup
// instructions. Preference order:
//
//  1. APIGW_PUBLIC_HOST env (operator override — wins over everything).
//  2. First TLS domain (Let's Encrypt / DuckDNS issued — must already
//     resolve, otherwise the cert wouldn't have been issued).
//  3. cfg.Listen.ServerName when not the catch-all "_".
//  4. The hostname's first non-loopback routable IP — when ServerName="_"
//     (any-address) we still want a URL the operator can paste WITHOUT
//     editing. Better a bare IP than the literal "<your-host>" string
//     that broke GitHub deliveries when copied verbatim.
//  5. os.Hostname() — last resort before the placeholder.
func publicHost(cfg *config.Config) string {
	if v := strings.TrimSpace(os.Getenv("APIGW_PUBLIC_HOST")); v != "" {
		return v
	}
	if len(cfg.TLS.Domains) > 0 {
		return cfg.TLS.Domains[0]
	}
	if cfg.Listen.ServerName != "" && cfg.Listen.ServerName != "_" {
		return cfg.Listen.ServerName
	}
	if ip := firstRoutableIP(); ip != "" {
		return ip
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "<your-host>"
}

// firstRoutableIP returns the first non-loopback, non-link-local IP from
// any UP interface. Empty string on no candidate. IPv4 wins over IPv6 so
// the printed URL looks familiar to the operator.
func firstRoutableIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var v4, v6 string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil && v4 == "" {
				v4 = ip4.String()
			} else if ip.To4() == nil && v6 == "" {
				v6 = "[" + ip.String() + "]"
			}
		}
	}
	if v4 != "" {
		return v4
	}
	return v6
}

func boolLabel(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func repoSettingsURL(repo string) string {
	// Best-effort: turn https://github.com/owner/repo[.git] into a settings/hooks URL.
	// SSH URLs and self-hosted GH endpoints fall back to a generic suggestion.
	prefix := "https://github.com/"
	if len(repo) > len(prefix) && repo[:len(prefix)] == prefix {
		path := repo[len(prefix):]
		if n := len(path); n > 4 && path[n-4:] == ".git" {
			path = path[:n-4]
		}
		return prefix + path + "/settings/hooks/new"
	}
	return "<repo>/settings/hooks/new"
}
