// Package rollback implements `apigw deploy rollback <name>` — flip the
// current symlink back to the previous release without re-cloning.
//
// This is the emergency-revert path: a deploy passed health checks but
// is misbehaving under real traffic and the operator needs to revert
// in seconds. Build state is preserved on disk (PruneOldReleases keeps
// the last N), so we just flip the symlink and reload the unit.
package rollback

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdRollback(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <name>",
		Short: "Revert a deploy to its previous release (no rebuild)",
		Long: "Flips the deploy's current symlink to the previous release\n" +
			"directory (by mtime) and triggers a systemd reload. The old\n" +
			"binary is reused, so this is fast and the next webhook still\n" +
			"deploys the latest commit. Use to recover from a known-bad\n" +
			"release while you debug the root cause.",
		Example: `  $ sudo apigw deploy rollback hello
  $ sudo apigw deploy rollback hello --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := f.Config()
			if err != nil {
				return err
			}
			cfg, ok := c.(*config.Config)
			if !ok {
				return fmt.Errorf("config: unexpected type %T", c)
			}
			if cfg.FindDeploy(name) == nil {
				return tui.NewError(
					fmt.Sprintf("no deploy named %q", name),
					"check `apigw deploy list` for the right name")
			}
			prev, err := deploy.RollbackToPrevious(name)
			if err != nil {
				return tui.NewError("rollback failed", err.Error()).
					WithFix("apigw deploy status "+name, "see current state").
					WithFix("ls /var/lib/apigw/"+name+"/releases", "verify what releases are on disk")
			}
			_ = cfg.SetDeployStatus(name, prev, "ok", "rolled back via CLI")
			_ = cfg.Save()
			fmt.Fprintf(f.IOStreams.Out, "%s rolled back %s → %s\n",
				tui.Styles.Success.Render(tui.GlyphCheck),
				tui.Styles.Identifier.Render(name),
				tui.Styles.Identifier.Render(prev))
			fmt.Fprintln(f.IOStreams.Out, "  Reload the unit if it isn't watching the symlink:")
			fmt.Fprintf(f.IOStreams.Out, "    sudo systemctl restart apigw-deploy@%s\n", name)
			return nil
		},
	}
}
