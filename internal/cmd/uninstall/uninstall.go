// Package uninstall implements `apigw uninstall` — the reciprocal of
// `apigw install`. Stops + disables every unit apigw owns, removes them,
// strips the api-manage shim, and (with --purge) wipes state directories.
//
// Auto-backs up to <cwd>/apigw-pre-uninstall-<ts>.tar.gz before touching
// anything. Skippable via --skip-backup for CI / scripted teardown.
package uninstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	apibackup "github.com/devevghenicernev-png/apigw/internal/backup"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	apideploy "github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/shim"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/tuning"
)

type options struct {
	f *cmdutil.Factory

	Purge      bool
	SkipBackup bool
	BackupOut  string
	Yes        bool
	DryRun     bool
}

func NewCmdUninstall(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove apigw units, shim, and (optionally) state",
		Long: "Stops every unit apigw owns (dashboard, webhook, tls-renew\n" +
			"timer, deploy@*), removes their unit files, drops the api-manage\n" +
			"compatibility symlink, and (with --purge) wipes /etc/apigw and\n" +
			"/var/lib/apigw. Auto-backs up the running state first.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.Yes, _ = c.Flags().GetBool("yes")
			opts.DryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Purge, "purge", false,
		"also delete /etc/apigw and /var/lib/apigw (destructive)")
	cmd.Flags().BoolVar(&opts.SkipBackup, "skip-backup", false,
		"skip the auto-backup taken before any removal")
	cmd.Flags().StringVar(&opts.BackupOut, "backup-out", "",
		"path for the pre-uninstall backup (default apigw-pre-uninstall-<ts>.tar.gz)")
	return cmd
}

func run(opts *options) error {
	cfg, cfgErr := config.FromFactory(opts.f)
	// cfgErr is non-fatal — we still want uninstall to work on a partially
	// broken install. Surface as a warning when we render the plan.

	ios := opts.f.IOStreams

	deploys := []string{}
	if cfg != nil {
		for _, d := range cfg.Deploys {
			deploys = append(deploys, d.Name)
		}
	}

	plan := tui.NewPlan("Uninstall apigw").
		Add("Dashboard service", "stop + disable + remove unit").
		Add("Webhook service", "stop + disable + remove unit").
		Add("TLS renew timer", "stop + disable + remove unit").
		Add("Deploy units", fmt.Sprintf("%d (stop + disable + remove)", len(deploys))).
		Add("api-manage shim", "remove /usr/local/bin/api-manage").
		Add("nginx site", "remove apigw.conf + reload").
		Add("State (/etc/apigw, /var/lib/apigw)", purgeLabel(opts.Purge))
	if !opts.SkipBackup && !opts.Purge && cfgErr == nil {
		plan.Add("Pre-uninstall backup", "tar.gz of /etc/apigw + /var/lib/apigw/{acme,certs}")
	}
	if cfgErr != nil {
		plan.Add("Config", tui.Styles.Warn.Render("could not load — proceeding best-effort"))
	}
	plan.Render(ios.Out, ios)

	if opts.DryRun {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("\ndry-run: nothing was changed."))
		return nil
	}

	// Per ARCHITECTURE.md §"CLI design system" rule 4: uninstall requires
	// TypeToConfirm. We accept --yes only when there's no TTY (CI use case);
	// the interactive path always demands the hostname back.
	if ios.IsStdinTTY() {
		host, _ := os.Hostname()
		if host == "" {
			host = "apigw"
		}
		fmt.Fprintf(ios.Out, "\n%s this will remove every apigw unit on %s\n",
			tui.Styles.Warn.Render("!"),
			tui.Styles.Identifier.Render(host))
		if err := opts.f.Prompter.TypeToConfirm(
			fmt.Sprintf("Type %q to confirm", host), host,
		); err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return cmdutil.SilentError
		}
	} else if !opts.Yes {
		return tui.NewError(
			"uninstall requires confirmation",
			"running headlessly — pass --yes to confirm, or attach a TTY",
		).WithDocs("E_UNINSTALL_NO_TTY")
	}

	// ----- backup -----
	if !opts.SkipBackup {
		dest := opts.BackupOut
		if dest == "" {
			dest = fmt.Sprintf("apigw-pre-uninstall-%s.tar.gz",
				time.Now().UTC().Format("20060102-150405"))
		}
		if _, err := apibackup.Pack(dest, apibackup.PackOptions{}); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s pre-uninstall backup failed: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(ios.Out, "%s pre-uninstall backup: %s\n",
				tui.Styles.Success.Render(tui.GlyphCheck),
				tui.Styles.Identifier.Render(dest))
		}
	}

	// ----- stop + remove the daemon-style units (idempotent) -----
	bestEffort := func(label string, fn func() error) {
		if err := fn(); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s %s: %s\n",
				tui.Styles.Warn.Render("!"), label, err.Error())
			return
		}
		fmt.Fprintf(ios.Out, "%s %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck), label)
	}
	bestEffort("dashboard service removed", system.UninstallDashboardUnit)
	bestEffort("webhook service removed", system.UninstallWebhookUnit)
	bestEffort("tls renew timer removed", system.UninstallTLSRenewTimer)

	for _, name := range deploys {
		n := name
		bestEffort("deploy "+n+" disabled", func() error { return apideploy.Disable(n) })
	}

	// ----- shim symlink -----
	shimPath := "/usr/local/bin/" + shim.LegacyBinary
	if st, err := os.Lstat(shimPath); err == nil && st.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(shimPath); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s could not remove api-manage shim: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(ios.Out, "%s api-manage shim removed\n",
				tui.Styles.Success.Render(tui.GlyphCheck))
		}
	}

	// ----- nginx site -----
	// Remove the server-scope conf + its enabled-link symlink, the
	// http-scope conf in conf.d/ (and our backup), and the stream-scope
	// file outside conf.d/. Leaving any of these behind left the operator
	// with stale apigw upstreams/log_formats long after uninstall.
	for _, p := range []string{
		nginx.SitePath, nginx.EnabledLink, nginx.SitePath + nginx.BackupExtension,
		nginx.HTTPConfPath, nginx.HTTPConfPath + nginx.BackupExtension,
		nginx.StreamConfPath, nginx.StreamConfPath + nginx.BackupExtension,
	} {
		_ = os.Remove(p)
	}
	// Strip any leftover apigw marker blocks from /etc/nginx/nginx.conf
	// (tuning, stream-include). Best-effort — the next `apigw install`
	// would re-inject them if needed.
	_, _ = tuning.Revert()
	// Restore the stock default site we disabled on install (BUG-6). The
	// re-symlink covers the common case; if a real file was backed up to
	// .apigw-prev we rename it back. Both are best-effort.
	defaultLink := paths.NginxSitesEnabled() + "/default"
	defaultSrc := paths.NginxSitesAvailable() + "/default"
	if _, err := os.Lstat(defaultLink); errors.Is(err, os.ErrNotExist) {
		if _, srcErr := os.Stat(defaultSrc); srcErr == nil {
			_ = os.Symlink(defaultSrc, defaultLink)
		}
		if _, bakErr := os.Stat(defaultLink + ".apigw-prev"); bakErr == nil {
			_ = os.Rename(defaultLink+".apigw-prev", defaultLink)
		}
	}
	if err := system.Run("systemctl", "reload", "nginx"); err == nil {
		fmt.Fprintf(ios.Out, "%s nginx site removed + reloaded\n",
			tui.Styles.Success.Render(tui.GlyphCheck))
	}

	// ----- purge state -----
	if opts.Purge {
		for _, p := range []string{"/etc/apigw", "/var/lib/apigw"} {
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(ios.ErrOut, "%s could not purge %s: %s\n",
					tui.Styles.Warn.Render("!"), p, err.Error())
			} else {
				fmt.Fprintf(ios.Out, "%s purged %s\n",
					tui.Styles.Success.Render(tui.GlyphCheck),
					tui.Styles.Identifier.Render(p))
			}
		}
		// Best-effort: purge per-deploy env files we wrote into /etc/apigw.
		for _, name := range deploys {
			_ = os.Remove(filepath.Join("/etc/apigw", name+".env"))
		}
	}

	fmt.Fprintln(ios.Out)
	fmt.Fprintf(ios.Out, "%s apigw uninstalled\n",
		tui.Styles.Success.Render(tui.GlyphCheck))
	if !opts.Purge {
		fmt.Fprintf(ios.Out, "  %s state preserved at %s + %s\n",
			tui.Styles.Muted.Render("›"),
			tui.Styles.Identifier.Render("/etc/apigw"),
			tui.Styles.Identifier.Render("/var/lib/apigw"))
	}
	return nil
}

func purgeLabel(p bool) string {
	if p {
		return "delete (--purge — destructive)"
	}
	return "keep on disk for re-install"
}
