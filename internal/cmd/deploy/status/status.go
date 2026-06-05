// Package status implements `apigw deploy status [<name>]`.
package status

import (
	"encoding/json"
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [<name>]",
		Short: "Show one or all deployments' state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return run(f, name, asJSON)
		},
	}
	return cmd
}

type record struct {
	Name       string `json:"name"`
	Active     bool   `json:"systemd_active"`
	LastSHA    string `json:"last_sha"`
	LastDeploy string `json:"last_deploy"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error,omitempty"`
	Path       string `json:"nginx_path"`
	Port       int    `json:"port"`
	Runtime    string `json:"runtime"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
}

func run(f *cmdutil.Factory, name string, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	deploys := cfg.Deploys
	if name != "" {
		d := cfg.FindDeploy(name)
		if d == nil {
			return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
				WithDocs("E_DEPLOY_NOT_FOUND")
		}
		deploys = []config.Deploy{*d}
	}
	out := make([]record, 0, len(deploys))
	for _, d := range deploys {
		out = append(out, record{
			Name:       d.Name,
			Active:     deploy.IsActive(d.Name),
			LastSHA:    d.LastSHA,
			LastDeploy: d.LastDeploy,
			LastStatus: d.LastStatus,
			LastError:  d.LastError,
			Path:       d.Path,
			Port:       d.Port,
			Runtime:    d.Runtime,
			Repo:       d.Repo,
			Branch:     d.Branch,
		})
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(f.IOStreams.Out, tui.Styles.Muted.Render("No deployments registered."))
		return nil
	}
	for _, r := range out {
		plan := tui.NewPlan(r.Name).
			Add("Runtime", r.Runtime).
			Add("Repo", r.Repo).
			Add("Branch", r.Branch).
			Add("Port", fmt.Sprintf("%d", r.Port)).
			Add("Nginx mount", r.Path).
			Add("Last SHA", short(r.LastSHA)).
			Add("Last deploy", emptyDash(r.LastDeploy)).
			Add("Status", r.LastStatus).
			Add("Systemd", activeLabel(r.Active))
		if r.LastError != "" {
			plan.Add("Last error", r.LastError)
		}
		plan.Render(f.IOStreams.Out, f.IOStreams)
		fmt.Fprintln(f.IOStreams.Out)
	}
	return nil
}

func short(s string) string {
	if len(s) <= 7 {
		return emptyDash(s)
	}
	return s[:7]
}
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func activeLabel(a bool) string {
	if a {
		return "active"
	}
	return "inactive"
}
