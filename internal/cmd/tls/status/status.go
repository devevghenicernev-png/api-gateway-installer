// Package status implements `apigw tls status`.
package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f      *cmdutil.Factory
	asJSON bool
}

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current TLS configuration and certificate expiry",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.asJSON, _ = c.Flags().GetBool("json")
			return run(opts)
		},
	}
	return cmd
}

func run(opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}

	certs, err := apitls.ListCerts()
	if err != nil {
		return fmt.Errorf("list certs: %w", err)
	}

	if opts.asJSON {
		payload := struct {
			Strategy string             `json:"strategy"`
			Domains  []string           `json:"domains"`
			Staging  bool               `json:"staging"`
			Certs    []apitls.CertInfo  `json:"certs"`
		}{
			Strategy: cfg.TLS.Strategy,
			Domains:  cfg.TLS.Domains,
			Staging:  cfg.TLS.Staging,
			Certs:    certs,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(opts.f.IOStreams.Out, string(b))
		return nil
	}

	ios := opts.f.IOStreams
	strategy := cfg.TLS.Strategy
	if strategy == "" {
		strategy = "none"
	}

	plan := tui.NewPlan("TLS status").
		Add("Strategy", strategy).
		Add("Domains", emptyDash(strings.Join(cfg.TLS.Domains, ", ")))
	if cfg.TLS.Staging {
		plan.Add("CA", "staging (not browser-trusted)")
	}
	plan.Render(ios.Out, ios)

	if len(certs) == 0 {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("\nNo certificates installed. Run `apigw tls enable` to start."))
		return nil
	}

	fmt.Fprintln(ios.Out)
	for _, ci := range certs {
		label := fmt.Sprintf("%-26s", ci.Domain)
		expiry := ci.NotAfter.Format("2006-01-02")
		days := ci.DaysLeft
		var glyph, daysStr string
		switch {
		case days < 0:
			glyph = tui.Styles.Danger.Render(tui.GlyphCross)
			daysStr = tui.Styles.Danger.Render(fmt.Sprintf("expired %d days ago", -days))
		case days < 30:
			glyph = tui.Styles.Warn.Render("!")
			daysStr = tui.Styles.Warn.Render(fmt.Sprintf("%d days left", days))
		default:
			glyph = tui.Styles.Success.Render(tui.GlyphCheck)
			daysStr = tui.Styles.Success.Render(fmt.Sprintf("%d days left", days))
		}
		fmt.Fprintf(ios.Out, "%s %s  %s  %s  %s\n",
			glyph,
			tui.Styles.Identifier.Render(label),
			tui.Styles.Muted.Render("expires "+expiry),
			daysStr,
			tui.Styles.Muted.Render("("+string(ci.Strategy)+")"))
	}

	// Note next-renewal window for context.
	next := nextWindow()
	fmt.Fprintf(ios.Out, "\n%s next auto-renewal check: %s\n",
		tui.Styles.Muted.Render("›"),
		tui.Styles.Muted.Render(next))
	return nil
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func nextWindow() string {
	// Twice-daily timer fires at 03:00 and 15:00 local with ±12h jitter.
	// Show the *bound* of the next window — when the timer is *earliest*.
	now := time.Now()
	candidates := []time.Time{
		time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location()),
		time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location()),
	}
	for _, t := range candidates {
		if t.After(now) {
			return t.Format("Mon 15:04 MST")
		}
	}
	tomorrow := candidates[0].Add(24 * time.Hour)
	return tomorrow.Format("Mon 15:04 MST")
}
