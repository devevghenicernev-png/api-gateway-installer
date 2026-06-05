// Package backup implements `apigw backup`.
package backup

import (
	"fmt"
	"path/filepath"
	"time"

	apibackup "github.com/devevghenicernev-png/apigw/internal/backup"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	Out          string
	IncludeQueue bool
}

func NewCmdBackup(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Save apigw state to a tar.gz",
		Long: "Backs up: /etc/apigw, /var/lib/apigw/acme, /var/lib/apigw/certs.\n" +
			"Optionally the bbolt webhook queue (--include-queue). Release dirs\n" +
			"and logs are NOT backed up — they rebuild from git / journald.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Out, "out", "",
		"output path (default: apigw-backup-<timestamp>.tar.gz in cwd)")
	cmd.Flags().BoolVar(&opts.IncludeQueue, "include-queue", false,
		"include the bbolt webhook queue (off by default — restoring it can re-trigger old deploys)")
	return cmd
}

func run(opts *options) error {
	dest := opts.Out
	if dest == "" {
		dest = fmt.Sprintf("apigw-backup-%s.tar.gz",
			time.Now().UTC().Format("20060102-150405"))
	}
	abs, _ := filepath.Abs(dest)

	mf, err := apibackup.Pack(dest, apibackup.PackOptions{IncludeQueue: opts.IncludeQueue})
	if err != nil {
		return tui.NewError("backup failed", err.Error()).
			WithDocs("E_BACKUP")
	}

	ios := opts.f.IOStreams
	plan := tui.NewPlan("Backup created").
		Add("Path", abs).
		Add("Schema", fmt.Sprintf("v%d", mf.SchemaVersion)).
		Add("Files", fmt.Sprintf("%d", len(mf.Files))).
		Add("Queue included", yesNo(mf.IncludesQueue))
	plan.Render(ios.Out, ios)

	fmt.Fprintln(ios.Out)
	fmt.Fprintf(ios.Out, "%s restore with `apigw restore %s`\n",
		tui.Styles.Accent.Render(tui.GlyphArrow), dest)
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
