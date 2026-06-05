// Package migrate implements `apigw migrate` — the one-shot translation
// from the legacy bash installer to apigw. Auto-backs up the new state
// before writing, leaves the old apis.json renamed-but-present for safety.
//
// Idempotent: a second invocation on an already-migrated host returns "no
// changes needed" rather than re-importing duplicates.
package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	apibackup "github.com/devevghenicernev-png/apigw/internal/backup"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/iostreams"
	migrateinternal "github.com/devevghenicernev-png/apigw/internal/migrate"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/shim"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	DryRun        bool
	Force         bool
	SkipBackup    bool
	SkipShim      bool
	BackupOut     string
	Yes           bool
}

func NewCmdMigrate(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Import a legacy bash-installer state into apigw",
		Long: "Reads /etc/api-gateway/apis.json + deployments/*.json, translates\n" +
			"them into apigw config, and installs the api-manage compatibility\n" +
			"shim symlink. Auto-backs up the apigw config before writing.\n\n" +
			"Idempotent: a second run on an already-migrated host is a no-op.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.Yes, _ = c.Flags().GetBool("yes")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show what would change; touch nothing")
	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"merge into an existing apigw config even on name collision")
	cmd.Flags().BoolVar(&opts.SkipBackup, "skip-backup", false,
		"skip the auto-backup of /etc/apigw before writing")
	cmd.Flags().BoolVar(&opts.SkipShim, "skip-shim", false,
		"don't install /usr/local/bin/api-manage compatibility symlink")
	cmd.Flags().StringVar(&opts.BackupOut, "backup-out", "",
		"path for the pre-migration backup (default apigw-pre-migrate-<ts>.tar.gz)")
	return cmd
}

func run(opts *options) error {
	det, err := migrateinternal.Detect()
	if err != nil {
		return err
	}
	if !det.IsBashInstall() {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render(
			"No legacy installation detected — nothing to migrate."))
		return nil
	}

	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}

	apis, err := migrateinternal.ReadAPIsFile(det.APIsFile)
	if err != nil {
		return tui.NewError("read legacy apis.json", err.Error()).
			WithDocs("E_MIGRATE_READ")
	}
	deploys, err := migrateinternal.ReadDeployments(det.DeployDir)
	if err != nil {
		return tui.NewError("read legacy deployments", err.Error()).
			WithDocs("E_MIGRATE_READ")
	}

	plan := migrateinternal.Transform(cfg, apis, deploys)

	ios := opts.f.IOStreams
	renderPlan(ios, det, plan, opts)

	if plan.IsEmpty() {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render(
			"\nAll legacy entries already match the current config — nothing to do."))
		return nil
	}

	if opts.DryRun {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("\ndry-run: nothing was changed."))
		return nil
	}

	if !opts.Yes && ios.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Apply migration?", "", true)
		if err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return err
		}
		if !ok {
			return cmdutil.SilentError
		}
	}

	// Auto-backup the NEW side before we touch it. The bash side is
	// untouched until the very end (a rename, not a delete).
	if !opts.SkipBackup {
		dest := opts.BackupOut
		if dest == "" {
			dest = fmt.Sprintf("apigw-pre-migrate-%s.tar.gz",
				time.Now().UTC().Format("20060102-150405"))
		}
		if _, err := apibackup.Pack(dest, apibackup.PackOptions{}); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s pre-migration backup failed: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(ios.Out, "%s pre-migration backup: %s\n",
				tui.Styles.Success.Render(tui.GlyphCheck),
				tui.Styles.Identifier.Render(dest))
		}
	}

	if err := migrateinternal.Apply(cfg, plan, deploys); err != nil {
		return tui.NewError("migration failed", err.Error()).
			WithDocs("E_MIGRATE")
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}

	// Rename — never delete — the legacy apis.json so the operator can
	// inspect it / roll back manually.
	if det.HasAPIsFile {
		stamp := time.Now().UTC().Format("20060102-150405")
		moved := det.APIsFile + ".migrated-" + stamp
		if err := os.Rename(det.APIsFile, moved); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s could not rename legacy apis.json: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(ios.Out, "%s legacy apis.json saved as %s\n",
				tui.Styles.Muted.Render("›"), filepath.Base(moved))
		}
	}

	if !opts.SkipShim {
		if err := installShim(); err != nil {
			fmt.Fprintf(ios.ErrOut, "%s could not install api-manage shim: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		} else {
			fmt.Fprintf(ios.Out, "%s api-manage symlink installed (legacy CLI still works)\n",
				tui.Styles.Success.Render(tui.GlyphCheck))
		}
	}

	fmt.Fprintln(ios.Out)
	fmt.Fprintf(ios.Out, "%s migration complete: %d API(s) + %d deploy(s)\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		len(plan.APIsAdded), len(plan.DeploysAdded))
	fmt.Fprintf(ios.Out, "  %s apigw doctor\n",
		tui.Styles.Accent.Render("Next:"))
	return nil
}

func renderPlan(ios *iostreams.IOStreams, det migrateinternal.Detected, plan migrateinternal.Plan, opts *options) {
	w := ios.Out
	p := tui.NewPlan("Migrate from bash installer").
		Add("Legacy root", det.Root).
		Add("Legacy APIs", fmt.Sprintf("%d", det.APICount)).
		Add("Legacy deploys", fmt.Sprintf("%d", det.DeployCount))
	if len(plan.APIsAdded) > 0 {
		p.Add("Will import APIs", fmt.Sprintf("%d", len(plan.APIsAdded)))
	}
	if len(plan.DeploysAdded) > 0 {
		p.Add("Will import deploys", fmt.Sprintf("%d", len(plan.DeploysAdded)))
	}
	if len(plan.APIsAlreadyKnown) > 0 {
		p.Add("Skip (already present)", fmt.Sprintf("%d API(s)", len(plan.APIsAlreadyKnown)))
	}
	if len(plan.DeploysAlreadyKnown) > 0 {
		p.Add("Skip (already present)", fmt.Sprintf("%d deploy(s)", len(plan.DeploysAlreadyKnown)))
	}
	if len(plan.SecretsWritten) > 0 {
		p.Add("Webhook secrets to import", fmt.Sprintf("%d", len(plan.SecretsWritten)))
	}
	if len(plan.AIRegistered) > 0 {
		p.Add("AI providers recognised", fmt.Sprintf("%v", plan.AIRegistered))
	}
	if !opts.SkipShim {
		p.Add("api-manage shim", "/usr/local/bin/api-manage → apigw")
	}
	p.Render(w, ios)
}

// installShim creates the /usr/local/bin/api-manage symlink. Idempotent —
// repeats are no-ops; existing non-symlinks are left alone (operator may
// have a custom override).
func installShim() error {
	target, err := selfBinary()
	if err != nil {
		return err
	}
	link := "/usr/local/bin/" + shim.LegacyBinary
	st, err := os.Lstat(link)
	if err == nil {
		// Existing entry — if it's already a symlink to us, no-op.
		if st.Mode()&os.ModeSymlink != 0 {
			if cur, _ := os.Readlink(link); cur == target {
				return nil
			}
			// Different symlink target — replace.
			if err := os.Remove(link); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("%s exists and is not a symlink — refusing to overwrite", link)
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

// selfBinary returns the absolute path to the running apigw binary.
func selfBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil
	}
	return abs, nil
}
