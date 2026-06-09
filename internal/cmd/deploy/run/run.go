// Package run implements `apigw deploy run` — force a redeploy.
package run

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdRun(f *cmdutil.Factory) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Trigger a redeploy",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw deploy run hello
  $ apigw deploy run hello --force        # rebuild even if SHA is current`,
		RunE: func(c *cobra.Command, args []string) error {
			return runOne(c.Context(), f, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "rebuild even if the symlink already points at HEAD")
	return cmd
}

func runOne(ctx context.Context, f *cmdutil.Factory, name string, force bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	d := cfg.FindDeploy(name)
	if d == nil {
		return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
			WithFix("apigw deploy list", "see what's registered").
			WithDocs("E_DEPLOY_NOT_FOUND")
	}

	// Plan card per design rule 3 — even though deploy is single-target,
	// the mutation footprint (clone → build → swap → reload) is large
	// enough to warrant the explicit confirmation step.
	plan := tui.NewPlan("Redeploy").
		Add("Name", name).
		Add("Repo", d.Repo).
		Add("Branch", branchLabel(d.Branch)).
		Add("Port", fmt.Sprintf("%d", d.Port)).
		Add("Force clone", boolLabel(force))
	plan.Render(f.IOStreams.Out, f.IOStreams)

	fmt.Fprintf(f.IOStreams.Out, "\n%s deploying %s …\n",
		tui.Styles.Muted.Render("›"), name)
	q := deploy.NewJobQueue()
	res, derr := q.Submit(ctx, deploy.ApplyRequest{
		Name:        d.Name,
		Repo:        d.Repo,
		Branch:      d.Branch,
		Port:        d.Port,
		RuntimeHint: d.Runtime,
		Build:       d.Build,
		Start:       d.Start,
		HealthPath:  d.HealthPath,
		Logsink:     f.IOStreams.Out,
		ForceClone:  force,
	}, "")

	if derr != nil {
		_ = cfg.SetDeployStatus(name, "", "failed", derr.Error())
		_ = cfg.Save()
		return tui.NewError("deploy failed", derr.Error()).
			WithFix(fmt.Sprintf("apigw deploy logs %s", name), "inspect build output").
			WithDocs("E_DEPLOY_FAIL")
	}

	if res.Skipped {
		fmt.Fprintf(f.IOStreams.Out, "%s %s already at HEAD (%s) — nothing to do\n",
			tui.Styles.Muted.Render("›"),
			tui.Styles.Identifier.Render(name),
			shortSHA(res.SHA))
		return nil
	}

	_ = cfg.SetDeployStatus(name, res.SHA, "ok", "")
	_ = cfg.Save()

	// Re-render nginx in case the cached SHA shows up in the banner / static
	// alias path. Cheap, idempotent.
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).WithDocs("E_NGINX_RELOAD")
	}

	fmt.Fprintf(f.IOStreams.Out, "%s deployed %s @ %s (%.1fs)\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(name),
		shortSHA(res.SHA),
		res.Duration.Seconds())
	return nil
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

func branchLabel(b string) string {
	if b == "" {
		return "(default)"
	}
	return b
}

func boolLabel(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
