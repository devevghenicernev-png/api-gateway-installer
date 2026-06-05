// Package doctor implements `apigw doctor` — actionable health checklist.
//
// Returns exit code 0 (all good), 1 (warnings only), or 2 (one or more
// failures). Mirrors `gh auth status` UX: ✓/!/× glyphs, lowercase one-line
// messages, exact-command fixes.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/diag"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdDoctor(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose apigw with an actionable checklist",
		Long: "Runs every built-in check in parallel and prints results sorted\n" +
			"by severity (failures first). Exits 0 on all-ok, 1 on warnings,\n" +
			"2 on any failure — scriptable as `apigw doctor || apigw doctor --json | jq`.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			asJSON, _ := c.Flags().GetBool("json")
			return run(c, f, asJSON)
		},
	}
	return cmd
}

func run(cmd *cobra.Command, f *cmdutil.Factory, asJSON bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	checks := diag.All(cfg)
	runner := diag.Runner{Checks: checks}
	results := runner.Run(cmd.Context())

	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Fprintln(f.IOStreams.Out, string(b))
	} else {
		diag.SortByLevelThenName(results)
		renderTable(f, results)
	}

	// Exit codes — caller will pick these up via SilentError convention.
	worst := diag.Worst(results)
	switch worst {
	case diag.LevelFail:
		// Force a non-zero exit without re-printing.
		os.Exit(2) //nolint:gocritic // explicit by design — see Long above
	case diag.LevelWarn:
		os.Exit(1)
	}
	return nil
}

func renderTable(f *cmdutil.Factory, results []diag.Result) {
	ios := f.IOStreams
	ok, warn, fail := 0, 0, 0
	for _, r := range results {
		switch r.Level {
		case diag.LevelFail:
			fail++
		case diag.LevelWarn:
			warn++
		case diag.LevelOK:
			ok++
		}
	}
	// Header summary line.
	fmt.Fprintf(ios.Out, "%s %s · %s %s · %s %s\n\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Muted.Render(fmt.Sprintf("%d ok", ok)),
		tui.Styles.Warn.Render("!"),
		tui.Styles.Muted.Render(fmt.Sprintf("%d warn", warn)),
		tui.Styles.Danger.Render(tui.GlyphCross),
		tui.Styles.Muted.Render(fmt.Sprintf("%d fail", fail)))

	for _, r := range results {
		glyph, color := glyphFor(r.Level)
		fmt.Fprintf(ios.Out, "%s %s  %s\n",
			color(glyph),
			tui.Styles.Identifier.Render(r.Name),
			r.Message)
		if r.Fix != "" {
			fmt.Fprintf(ios.Out, "    %s %s\n",
				tui.Styles.Muted.Render("fix:"),
				tui.Styles.Accent.Render(r.Fix))
		}
	}
}

func glyphFor(l diag.Level) (string, func(string) string) {
	// lipgloss's Style.Render is variadic — wrap to satisfy our single-arg
	// callback signature.
	wrap := func(s lipgloss.Style) func(string) string {
		return func(in string) string { return s.Render(in) }
	}
	switch l {
	case diag.LevelOK:
		return tui.GlyphCheck, wrap(tui.Styles.Success)
	case diag.LevelInfo:
		return "·", wrap(tui.Styles.Muted)
	case diag.LevelWarn:
		return "!", wrap(tui.Styles.Warn)
	case diag.LevelFail:
		return tui.GlyphCross, wrap(tui.Styles.Danger)
	}
	return "?", wrap(tui.Styles.Muted)
}
