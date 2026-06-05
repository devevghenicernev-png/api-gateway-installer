// Package status implements `apigw ai status [provider]`.
package status

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [provider]",
		Short: "Show health and installed models per provider",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return run(c.Context(), f, name, asJSON)
		},
	}
	return cmd
}

type record struct {
	Provider   string   `json:"provider"`
	Port       int      `json:"port"`
	Path       string   `json:"path"`
	Registered bool     `json:"registered"`
	Listening  bool     `json:"listening"`
	Active     bool     `json:"systemd_active"`
	Models     []string `json:"models,omitempty"`
}

func run(ctx context.Context, f *cmdutil.Factory, providerArg string, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	var targets []ai.Provider
	if providerArg == "" {
		for _, a := range ai.ListAI(cfg) {
			if p := ai.ProviderOf(a); p != "" {
				targets = append(targets, p)
			}
		}
	} else {
		p, ok := ai.ParseProvider(providerArg)
		if !ok {
			return tui.NewError("unknown provider", fmt.Sprintf("got %q", providerArg)).
				WithDocs("E_AI_PROVIDER")
		}
		targets = []ai.Provider{p}
	}

	if len(targets) == 0 {
		fmt.Fprintln(f.IOStreams.Out, tui.Styles.Muted.Render(
			"No AI providers registered. Run `apigw ai add <provider>` first."))
		return nil
	}

	out := make([]record, 0, len(targets))
	for _, p := range targets {
		api := cfg.FindAPI(ai.APIName(p))
		port := p.Info().DefaultPort
		registered := api != nil
		if registered {
			port = api.Port
		}
		det, _ := ai.Detect(p, port)
		models, _ := ai.ListLocalModels(ctx, p)
		out = append(out, record{
			Provider:   string(p),
			Port:       port,
			Path:       ai.APIPath(p),
			Registered: registered,
			Listening:  det.Listening,
			Active:     det.Active,
			Models:     models,
		})
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}

	ios := f.IOStreams
	for _, r := range out {
		plan := tui.NewPlan(r.Provider).
			Add("Mount", r.Path).
			Add("Port", fmt.Sprintf("%d", r.Port)).
			Add("Registered", yesNo(r.Registered)).
			Add("Listening", yesNo(r.Listening)).
			Add("Systemd active", yesNo(r.Active))
		if len(r.Models) > 0 {
			plan.Add("Models", joinModels(r.Models))
		}
		plan.Render(ios.Out, ios)
		fmt.Fprintln(ios.Out)
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func joinModels(m []string) string {
	if len(m) == 0 {
		return "—"
	}
	out := m[0]
	for i := 1; i < len(m); i++ {
		out += ", " + m[i]
	}
	return out
}
