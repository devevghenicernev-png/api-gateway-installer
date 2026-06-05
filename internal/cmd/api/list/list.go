// Package list implements `apigw api list`.
package list

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

type options struct {
	f       *cmdutil.Factory
	asJSON  bool
	quiet   bool
	enabled string // "", "true", "false" — filter
}

func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered APIs",
		Args:  cobra.NoArgs,
		Example: `  $ apigw api list
  $ apigw api list --json | jq '.[].name'
  $ apigw api list --enabled=false`,
		RunE: func(c *cobra.Command, _ []string) error {
			opts.asJSON, _ = c.Flags().GetBool("json")
			opts.quiet, _ = c.Flags().GetBool("quiet")
			return run(opts)
		},
	}
	cmd.Flags().String("enabled", "", "filter by enabled state (true|false)")
	return cmd
}

func run(opts *options) error {
	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}
	apis := cfg.APIs
	if opts.enabled != "" {
		filtered := apis[:0:0]
		want := opts.enabled == "true"
		for _, a := range apis {
			if a.Enabled == want {
				filtered = append(filtered, a)
			}
		}
		apis = filtered
	}
	if opts.asJSON {
		return emitJSON(opts, apis)
	}
	return emitTable(opts, apis)
}

func emitJSON(opts *options, apis []config.API) error {
	b, err := json.MarshalIndent(apis, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(opts.f.IOStreams.Out, string(b))
	return nil
}

func emitTable(opts *options, apis []config.API) error {
	ios := opts.f.IOStreams
	if len(apis) == 0 {
		if !opts.quiet {
			fmt.Fprintln(ios.Out, tui.Styles.Muted.Render("No APIs registered. Add one with `apigw api add <name> --port <p>`."))
		}
		return nil
	}
	if opts.quiet {
		// -q contract: only IDs to stdout, errors to stderr.
		for _, a := range apis {
			fmt.Fprintln(ios.Out, a.Name)
		}
		return nil
	}

	// Width-pad columns manually — keep dependency footprint small.
	headers := []string{"NAME", "PORT", "PATH", "STATUS", "DESCRIPTION"}
	rows := make([][]string, 0, len(apis))
	for _, a := range apis {
		path := a.Path
		if path == "" {
			path = "/api/" + a.Name
		}
		status := tui.Styles.Success.Render("enabled")
		if !a.Enabled {
			status = tui.Styles.Muted.Render("disabled")
		}
		if !ios.ColorEnabled() {
			status = "enabled"
			if !a.Enabled {
				status = "disabled"
			}
		}
		rows = append(rows, []string{
			a.Name,
			fmt.Sprintf("%d", a.Port),
			path,
			status,
			a.Description,
		})
	}
	printTable(opts, headers, rows)
	return nil
}

func printTable(opts *options, headers []string, rows [][]string) {
	w := opts.f.IOStreams.Out
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
		fmt.Fprint(w, tui.Styles.Muted.Render(padCol(h, widths[i])))
		if i < len(headers)-1 {
			fmt.Fprint(w, "  ")
		}
	}
	fmt.Fprintln(w)
	for _, r := range rows {
		for i, c := range r {
			fmt.Fprint(w, padCol(c, widths[i]))
			if i < len(r)-1 {
				fmt.Fprint(w, "  ")
			}
		}
		fmt.Fprintln(w)
	}
}

func padCol(s string, w int) string {
	n := displayLen(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// displayLen approximates the on-screen width of s by stripping ANSI escapes.
// Good enough for column padding; lipgloss does the same trick internally.
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
