// Package status implements `apigw dashboard status`.
package status

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show dashboard service state + SSE client count",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			return run(f, asJSON)
		},
	}
	return cmd
}

func run(f *cmdutil.Factory, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	active := exec.Command("systemctl", "is-active", "--quiet", "apigw-dashboard.service").Run() == nil

	out := map[string]any{
		"systemd_active": active,
		"listen_addr":    fmt.Sprintf(":%d", cfg.Dashboard.Port),
		"path":           cfg.Dashboard.Path,
		"enabled":        cfg.Dashboard.Enabled,
	}

	// Optionally hit /api/status for live SSE-client count etc.
	if active {
		if snapshot, err := fetchSnapshot(cfg.Dashboard.Port); err == nil {
			out["snapshot"] = snapshot
		}
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}

	ios := f.IOStreams
	plan := tui.NewPlan("Dashboard status").
		Add("Listen", out["listen_addr"].(string)).
		Add("nginx mount", emptyDash(cfg.Dashboard.Path)).
		Add("systemd", activeLabel(active))
	if snap, ok := out["snapshot"].(map[string]any); ok {
		if v, ok := snap["sse_clients"].(float64); ok {
			plan.Add("SSE clients", fmt.Sprintf("%d", int(v)))
		}
		if v, ok := snap["uptime_sec"].(float64); ok {
			plan.Add("Uptime", fmt.Sprintf("%ds", int(v)))
		}
	}
	plan.Render(ios.Out, ios)
	return nil
}

func fetchSnapshot(port int) (map[string]any, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func activeLabel(a bool) string {
	if a {
		return "active"
	}
	return "inactive"
}
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
