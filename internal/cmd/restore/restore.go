// Package restore implements `apigw restore <file.tar.gz>`.
package restore

import (
	"errors"
	"fmt"
	"path/filepath"

	apibackup "github.com/devevghenicernev-png/apigw/internal/backup"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	File         string
	Force        bool
	DryRun       bool
	IncludeQueue bool
	Yes          bool
}

func NewCmdRestore(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "restore <file.tar.gz>",
		Short: "Restore apigw state from a backup archive",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw restore apigw-backup-20260605-103000.tar.gz --dry-run
  $ sudo apigw restore apigw-backup-20260605-103000.tar.gz --force`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.File = args[0]
			opts.Yes, _ = c.Flags().GetBool("yes")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"overwrite existing files even if content differs")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"validate the archive and list files, write nothing")
	cmd.Flags().BoolVar(&opts.IncludeQueue, "include-queue", false,
		"restore jobs.db too (off by default — risk of re-triggering old deploys)")
	return cmd
}

func run(opts *options) error {
	abs, _ := filepath.Abs(opts.File)

	mf, err := apibackup.ReadManifest(opts.File)
	if err != nil {
		return tui.NewError("invalid backup archive", err.Error()).
			WithDocs("E_RESTORE_BAD")
	}

	ios := opts.f.IOStreams
	plan := tui.NewPlan("Restore from backup").
		Add("Archive", abs).
		Add("Created", mf.CreatedAt.Format("2006-01-02 15:04 MST")).
		Add("From host", mf.Host).
		Add("apigw version", mf.APIGWVersion).
		Add("Schema", fmt.Sprintf("v%d", mf.SchemaVersion)).
		Add("Files", fmt.Sprintf("%d", len(mf.Files))).
		Add("Mode", modeLabel(opts))
	plan.Render(ios.Out, ios)

	if opts.DryRun {
		if err := apibackup.Unpack(opts.File, apibackup.UnpackOptions{
			DryRun:       true,
			IncludeQueue: opts.IncludeQueue,
		}); err != nil {
			return tui.NewError("dry-run failed", err.Error()).WithDocs("E_RESTORE")
		}
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("\ndry-run: nothing was changed."))
		return nil
	}

	if !opts.Yes && ios.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Apply this restore?", "", false)
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

	if err := apibackup.Unpack(opts.File, apibackup.UnpackOptions{
		Force:        opts.Force,
		IncludeQueue: opts.IncludeQueue,
	}); err != nil {
		return tui.NewError("restore failed", err.Error()).
			WithFix(fmt.Sprintf("apigw restore %s --dry-run", opts.File),
				"check what would change").
			WithDocs("E_RESTORE")
	}

	// Best-effort reload: re-render nginx from the restored config so the
	// site file matches what's in /etc/apigw/config.yaml.
	if cfg, cerr := config.FromFactory(opts.f); cerr == nil {
		if rerr := nginx.NewManager().WriteAndReload(cfg); rerr != nil {
			fmt.Fprintf(ios.ErrOut, "%s could not reload nginx: %s\n",
				tui.Styles.Warn.Render("!"), rerr.Error())
		}
	}

	fmt.Fprintf(ios.Out, "\n%s restored %d files\n",
		tui.Styles.Success.Render(tui.GlyphCheck), len(mf.Files))
	fmt.Fprintf(ios.Out, "  %s apigw doctor\n",
		tui.Styles.Accent.Render("Next:"))
	return nil
}

func modeLabel(opts *options) string {
	switch {
	case opts.DryRun:
		return "dry-run"
	case opts.Force:
		return "force (overwrite divergent files)"
	default:
		return "fail on divergent files"
	}
}
