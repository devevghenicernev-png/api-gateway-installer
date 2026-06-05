// Package list implements `apigw deploy list`.
package list

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered deployments",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			quiet, _ := c.Flags().GetBool("quiet")
			return run(f, asJSON, quiet)
		},
	}
	return cmd
}

func run(f *cmdutil.Factory, asJSON, quiet bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(cfg.Deploys, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}
	if len(cfg.Deploys) == 0 {
		if !quiet {
			fmt.Fprintln(f.IOStreams.Out, tui.Styles.Muted.Render(
				"No deployments registered. Add one with `apigw deploy add <name> --repo <url> --port <p>`."))
		}
		return nil
	}
	if quiet {
		for _, d := range cfg.Deploys {
			fmt.Fprintln(f.IOStreams.Out, d.Name)
		}
		return nil
	}

	headers := []string{"NAME", "PORT", "PATH", "RUNTIME", "STATUS", "SHA", "REPO"}
	rows := make([][]string, 0, len(cfg.Deploys))
	for _, d := range cfg.Deploys {
		path := d.Path
		if path == "" {
			path = "/apps/" + d.Name
		}
		status := statusGlyph(d, f, deploy.IsActive(d.Name))
		rows = append(rows, []string{
			d.Name,
			intStr(d.Port),
			path,
			d.Runtime,
			status,
			shortSHA(d.LastSHA),
			compactRepo(d.Repo),
		})
	}
	printTable(f, headers, rows)
	return nil
}

func statusGlyph(d config.Deploy, f *cmdutil.Factory, active bool) string {
	if !f.IOStreams.ColorEnabled() {
		switch {
		case d.LastStatus == "failed":
			return "failed"
		case active:
			return "running"
		case d.LastStatus == "ok":
			return "ok"
		default:
			return d.LastStatus
		}
	}
	switch {
	case d.LastStatus == "failed":
		return tui.Styles.Danger.Render("failed")
	case active:
		return tui.Styles.Success.Render("running")
	case d.LastStatus == "ok":
		return tui.Styles.Success.Render("ok")
	default:
		return tui.Styles.Muted.Render(d.LastStatus)
	}
}

func compactRepo(r string) string {
	r = strings.TrimSuffix(r, ".git")
	for _, prefix := range []string{"https://github.com/", "git@github.com:"} {
		if strings.HasPrefix(r, prefix) {
			return "gh:" + r[len(prefix):]
		}
	}
	return r
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}

func intStr(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func printTable(f *cmdutil.Factory, headers []string, rows [][]string) {
	w := f.IOStreams.Out
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayLen(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if n := displayLen(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for i, h := range headers {
		fmt.Fprint(w, tui.Styles.Muted.Render(pad(h, widths[i])))
		if i < len(headers)-1 {
			fmt.Fprint(w, "  ")
		}
	}
	fmt.Fprintln(w)
	for _, r := range rows {
		for i, c := range r {
			fmt.Fprint(w, pad(c, widths[i]))
			if i < len(r)-1 {
				fmt.Fprint(w, "  ")
			}
		}
		fmt.Fprintln(w)
	}
}

func pad(s string, w int) string {
	n := displayLen(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func displayLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			n++
		}
	}
	return n
}
