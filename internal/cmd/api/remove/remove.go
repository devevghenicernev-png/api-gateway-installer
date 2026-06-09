// Package remove implements `apigw api remove`.
package remove

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f      *cmdutil.Factory
	name   string
	yes    bool
	dryRun bool
}

func NewCmdRemove(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:     "remove <name>",
		Short:   "Unregister an upstream API",
		Aliases: []string{"rm", "delete"},
		Args:    cobra.ExactArgs(1),
		Example: `  $ apigw api remove hello              # interactive — type the name to confirm
  $ apigw api remove hello --yes        # non-interactive`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.name = args[0]
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
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
	if cfg.FindAPI(opts.name) == nil {
		return tui.NewError(
			"api not found",
			fmt.Sprintf("no api named %q is registered", opts.name),
		).WithFix("apigw api list", "see what's registered").
			WithDocs("E_API_NOT_FOUND")
	}

	plan := tui.NewPlan("Remove API").
		Add("Name", opts.name).
		Add("Effect", "delete config entry + reload nginx")
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
			fmt.Fprintln(opts.f.IOStreams.ErrOut, tui.Styles.Muted.Render("aborted."))
			return cmdutil.SilentError
		}
	}

	// Capture BasicAuth path before removal so the cleanup loop below
	// has somewhere to look.
	var basicAuthFile string
	if api := cfg.FindAPI(opts.name); api != nil && api.BasicAuth != nil {
		basicAuthFile = api.BasicAuth.File
		if basicAuthFile == "" {
			basicAuthFile = filepath.Join(paths.ConfigDir(), "htpasswd."+opts.name)
		}
	}

	if err := cfg.RemoveAPI(opts.name); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Orphan-prune sidecar files that nginx no longer references. We
	// only touch files we KNOW we created — never anything operator
	// supplied via custom File: in the BasicAuth config.
	if basicAuthFile != "" {
		_ = os.Remove(basicAuthFile)
	}

	mgr := nginx.NewManager()
	if err := mgr.WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("apigw doctor", "diagnose nginx state").
			WithDocs("E_NGINX_RELOAD")
	}

	if !opts.f.IOStreams.Quiet() {
		fmt.Fprintf(opts.f.IOStreams.Out, "\n%s removed api %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			tui.Styles.Identifier.Render(opts.name),
		)
	}
	return nil
}
