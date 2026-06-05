// Package add implements `apigw api add`.
package add

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	Name        string
	Port        int
	Path        string
	Description string

	yes      bool
	dryRun   bool
	noReload bool
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

func NewCmdAdd(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a new upstream API",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw api add hello --port 8080
  $ apigw api add billing --port 9000 --path /v1/billing --description "Billing service"`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.Name = args[0]
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().IntVar(&opts.Port, "port", 0, "upstream port (required)")
	cmd.Flags().StringVar(&opts.Path, "path", "", "URL path to mount on (default /api/<name>)")
	cmd.Flags().StringVar(&opts.Description, "description", "", "human-readable description")
	cmd.Flags().BoolVar(&opts.noReload, "no-reload", false, "skip nginx reload (use `apigw api reload` later)")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func run(opts *options) error {
	if !nameRE.MatchString(opts.Name) {
		return tui.NewError(
			"invalid api name",
			fmt.Sprintf("name %q must be lowercase alphanumeric with hyphens, 2-32 chars", opts.Name),
		).WithFix("apigw api add my-service --port 8080", "use a valid name").
			WithDocs("E_INVALID_NAME")
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return tui.NewError(
			"invalid port",
			fmt.Sprintf("port %d is outside 1-65535", opts.Port),
		).WithDocs("E_INVALID_PORT")
	}

	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	if cfg.FindAPI(opts.Name) != nil {
		return tui.NewError(
			"api already exists",
			fmt.Sprintf("an api named %q is already registered", opts.Name),
		).WithFix("apigw api list", "see what's registered").
			WithFix(fmt.Sprintf("apigw api remove %s", opts.Name), "remove the existing one").
			WithDocs("E_API_EXISTS")
	}

	path := opts.Path
	if path == "" {
		path = "/api/" + opts.Name
	}
	plan := tui.NewPlan("Add API").
		Add("Name", opts.Name).
		Add("Port", fmt.Sprintf("%d", opts.Port)).
		Add("Path", path).
		Add("Description", emptyDash(opts.Description))
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}

	if !opts.yes && opts.f.IOStreams.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Apply this plan?", "", true)
		if err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return err
		}
		if !ok {
			fmt.Fprintln(opts.f.IOStreams.ErrOut, tui.Styles.Muted.Render("aborted."))
			return cmdutil.SilentError
		}
	}

	if err := cfg.AddAPI(config.API{
		Name:        opts.Name,
		Port:        opts.Port,
		Path:        path,
		Description: opts.Description,
		Enabled:     true,
	}); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if !opts.noReload {
		mgr := nginx.NewManager()
		if err := mgr.WriteAndReload(cfg); err != nil {
			return tui.NewError(
				"nginx reload failed",
				err.Error(),
			).WithFix("apigw doctor", "diagnose nginx state").
				WithDocs("E_NGINX_RELOAD")
		}
	}

	if !opts.f.IOStreams.Quiet() {
		fmt.Fprintf(opts.f.IOStreams.Out, "\n%s registered api %s on %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			tui.Styles.Identifier.Render(opts.Name),
			tui.Styles.Identifier.Render(path),
		)
	} else {
		// -q contract: print the ID to stdout.
		fmt.Fprintln(opts.f.IOStreams.Out, opts.Name)
	}
	return nil
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
