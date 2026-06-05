// Package add implements `apigw deploy add`.
//
// The flow: validate flags → register config entry → run the initial deploy
// (clone+detect+build+supervise+swap+health). The first add is also where
// the systemd template unit is installed lazily.
package add

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

type options struct {
	f *cmdutil.Factory

	Name        string
	Repo        string
	Branch      string
	Port        int
	Path        string
	Runtime     string
	Build       string
	Start       string
	Description string

	yes      bool
	dryRun   bool
	noStart  bool // register only, don't run the first deploy
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

func NewCmdAdd(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register and roll out a new deployment",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw deploy add hello --repo https://github.com/owner/hello --port 3000
  $ apigw deploy add api --repo git@github.com:owner/api --branch main --port 8080 --runtime node
  $ apigw deploy add docs --repo https://github.com/owner/docs --port 0 --runtime static --path /docs`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.Name = args[0]
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(c.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.Repo, "repo", "", "git URL (https or ssh) — required")
	cmd.Flags().StringVar(&opts.Branch, "branch", "main", "branch to deploy")
	cmd.Flags().IntVar(&opts.Port, "port", 0, "upstream port (0 = no listener, e.g. static or worker)")
	cmd.Flags().StringVar(&opts.Path, "path", "", "nginx mount path (default /apps/<name>)")
	cmd.Flags().StringVar(&opts.Runtime, "runtime", "auto", "auto|node|python|go|docker|static")
	cmd.Flags().StringVar(&opts.Build, "build", "", "override build command")
	cmd.Flags().StringVar(&opts.Start, "start", "", "override start command (ignored for static)")
	cmd.Flags().StringVar(&opts.Description, "description", "", "human-readable description")
	cmd.Flags().BoolVar(&opts.noStart, "no-start", false, "register the deployment but don't clone/build")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func run(ctx context.Context, opts *options) error {
	if !nameRE.MatchString(opts.Name) {
		return tui.NewError(
			"invalid deploy name",
			fmt.Sprintf("name %q must be lowercase alphanumeric with hyphens, 2-32 chars", opts.Name),
		).WithDocs("E_INVALID_NAME")
	}
	if opts.Port < 0 || opts.Port > 65535 {
		return tui.NewError("invalid port", fmt.Sprintf("port %d outside 0-65535", opts.Port)).
			WithDocs("E_INVALID_PORT")
	}

	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	if cfg.FindDeploy(opts.Name) != nil {
		return tui.NewError(
			"deploy already exists",
			fmt.Sprintf("a deployment named %q is already registered", opts.Name),
		).WithFix("apigw deploy list", "see what's registered").
			WithFix(fmt.Sprintf("apigw deploy remove %s", opts.Name), "remove the existing one").
			WithDocs("E_DEPLOY_EXISTS")
	}

	path := opts.Path
	if path == "" {
		path = "/apps/" + opts.Name
	}
	plan := tui.NewPlan("Add deployment").
		Add("Name", opts.Name).
		Add("Repo", opts.Repo).
		Add("Branch", opts.Branch).
		Add("Port", fmt.Sprintf("%d", opts.Port)).
		Add("Path", path).
		Add("Runtime", opts.Runtime).
		Add("First deploy", firstDeployLabel(opts.noStart))
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
			return cmdutil.SilentError
		}
	}

	d := config.Deploy{
		Name:        opts.Name,
		Repo:        opts.Repo,
		Branch:      opts.Branch,
		Port:        opts.Port,
		Path:        path,
		Runtime:     opts.Runtime,
		Build:       opts.Build,
		Start:       opts.Start,
		Description: opts.Description,
		Enabled:     true,
		LastStatus:  "pending",
	}
	if err := cfg.AddDeploy(d); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if opts.noStart {
		fmt.Fprintf(opts.f.IOStreams.Out, "\n%s registered (run `apigw deploy run %s` to deploy)\n",
			tui.Styles.Success.Render(tui.GlyphCheck), opts.Name)
		return nil
	}

	fmt.Fprintf(opts.f.IOStreams.Out, "\n%s deploying %s …\n",
		tui.Styles.Muted.Render("›"), opts.Name)
	q := deploy.NewJobQueue()
	res, derr := q.Submit(ctx, deploy.ApplyRequest{
		Name:        d.Name,
		Repo:        d.Repo,
		Branch:      d.Branch,
		Port:        d.Port,
		RuntimeHint: d.Runtime,
		Build:       d.Build,
		Start:       d.Start,
		Logsink:     opts.f.IOStreams.Out,
	}, "")

	if derr != nil {
		_ = cfg.SetDeployStatus(d.Name, "", "failed", derr.Error())
		_ = cfg.Save()
		return tui.NewError("first deploy failed", derr.Error()).
			WithFix(fmt.Sprintf("apigw deploy logs %s", d.Name), "inspect build output").
			WithFix(fmt.Sprintf("apigw deploy run %s", d.Name), "retry").
			WithDocs("E_DEPLOY_FAIL")
	}

	_ = cfg.SetDeployStatus(d.Name, res.SHA, "ok", "")
	_ = cfg.Save()

	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("apigw doctor", "diagnose nginx state").
			WithDocs("E_NGINX_RELOAD")
	}

	ios := opts.f.IOStreams
	fmt.Fprintf(ios.Out, "\n%s deployed %s @ %s (%.1fs)\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(opts.Name),
		shortSHA(res.SHA),
		res.Duration.Seconds())
	fmt.Fprintf(ios.Out, "  %s http://localhost%s\n",
		tui.Styles.Accent.Render(tui.GlyphArrow), path)
	return nil
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

func firstDeployLabel(skipped bool) string {
	if skipped {
		return "skipped (--no-start)"
	}
	return "clone + build + start"
}
