// Package remove implements `apigw deploy remove`.
package remove

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

type options struct {
	f      *cmdutil.Factory
	name   string
	yes    bool
	dryRun bool
	purge  bool
}

func NewCmdRemove(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Short:   "Stop and unregister a deployment",
		Aliases: []string{"rm", "delete"},
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			opts.name = args[0]
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.purge, "purge", false,
		"also delete /var/lib/apigw/<name>/ release history and /etc/apigw/<name>.env")
	return cmd
}

func run(opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	if cfg.FindDeploy(opts.name) == nil {
		return tui.NewError(
			"deploy not found",
			fmt.Sprintf("no deployment named %q is registered", opts.name),
		).WithFix("apigw deploy list", "see what's registered").
			WithDocs("E_DEPLOY_NOT_FOUND")
	}

	plan := tui.NewPlan("Remove deployment").
		Add("Name", opts.name).
		Add("Systemd unit", "stop + disable + uninstall drop-in").
		Add("nginx", "remove route + reload").
		Add("State", purgeLabel(opts.purge))
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}

	if !opts.yes && opts.f.IOStreams.IsStdinTTY() {
		if err := opts.f.Prompter.TypeToConfirm(
			fmt.Sprintf("Type %q to confirm removal", opts.name),
			opts.name,
		); err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return cmdutil.SilentError
		}
	}

	if err := deploy.Disable(opts.name); err != nil {
		fmt.Fprintf(opts.f.IOStreams.ErrOut, "%s could not disable unit: %s\n",
			tui.Styles.Warn.Render("!"), err.Error())
	}
	if err := cfg.RemoveDeploy(opts.name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}

	if opts.purge {
		if err := os.RemoveAll(deploy.DeployDir(opts.name)); err != nil {
			fmt.Fprintf(opts.f.IOStreams.ErrOut, "%s could not purge state dir: %s\n",
				tui.Styles.Warn.Render("!"), err.Error())
		}
		_ = os.Remove(deploy.EnvFile(opts.name))
		_ = os.Remove(filepath.Join("/var/log/apigw", opts.name))
		_ = webhook.RemoveSecret(opts.name)
	}
	fmt.Fprintf(opts.f.IOStreams.Out, "\n%s removed %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(opts.name))
	return nil
}

func purgeLabel(p bool) string {
	if p {
		return "purge releases + env file (--purge)"
	}
	return "keep on disk for re-add"
}
