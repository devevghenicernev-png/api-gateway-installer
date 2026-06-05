// Package list implements `apigw ai list`.
package list

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered AI providers",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			showAll, _ := c.Flags().GetBool("all")
			return run(f, asJSON, showAll)
		},
	}
	cmd.Flags().Bool("all", false, "include providers detected on this host but not yet registered")
	return cmd
}

type record struct {
	Provider   string `json:"provider"`
	Port       int    `json:"port"`
	Path       string `json:"path"`
	Registered bool   `json:"registered"`
	Listening  bool   `json:"listening"`
	Active     bool   `json:"systemd_active"`
}

func run(f *cmdutil.Factory, asJSON, showAll bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	registered := make(map[ai.Provider]config.API, 4)
	for _, a := range ai.ListAI(cfg) {
		if p := ai.ProviderOf(a); p != "" {
			registered[p] = a
		}
	}

	out := make([]record, 0, len(registered))
	for p, a := range registered {
		det, _ := ai.Detect(p, a.Port)
		out = append(out, record{
			Provider:   string(p),
			Port:       a.Port,
			Path:       a.Path,
			Registered: true,
			Listening:  det.Listening,
			Active:     det.Active,
		})
	}
	if showAll {
		for _, p := range ai.All() {
			if _, ok := registered[p]; ok {
				continue
			}
			det, _ := ai.Detect(p, 0)
			if !det.Installed() {
				continue
			}
			out = append(out, record{
				Provider:   string(p),
				Port:       det.Port,
				Path:       ai.APIPath(p),
				Registered: false,
				Listening:  det.Listening,
				Active:     det.Active,
			})
		}
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
		return nil
	}

	ios := f.IOStreams
	if len(out) == 0 {
		fmt.Fprintln(ios.Out, tui.Styles.Muted.Render(
			"No AI providers registered. Try `apigw ai list --all` to see what's installed."))
		return nil
	}

	headers := []string{"PROVIDER", "PORT", "PATH", "STATUS"}
	rows := make([][]string, 0, len(out))
	for _, r := range out {
		rows = append(rows, []string{
			r.Provider,
			fmt.Sprintf("%d", r.Port),
			r.Path,
			statusLabel(r, ios.ColorEnabled()),
		})
	}
	printTable(ios.Out, headers, rows)
	return nil
}

func statusLabel(r record, colored bool) string {
	var raw string
	switch {
	case !r.Registered && r.Listening:
		raw = "detected (run `apigw ai add " + r.Provider + "`)"
	case r.Listening && r.Active:
		raw = "listening + systemd active"
	case r.Listening:
		raw = "listening"
	case r.Active:
		raw = "systemd active, port silent"
	default:
		raw = "down"
	}
	if !colored {
		return raw
	}
	switch {
	case r.Listening:
		return tui.Styles.Success.Render(raw)
	case !r.Registered:
		return tui.Styles.Warn.Render(raw)
	default:
		return tui.Styles.Muted.Render(raw)
	}
}

func printTable(w interface{ Write(p []byte) (int, error) }, headers []string, rows [][]string) {
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
