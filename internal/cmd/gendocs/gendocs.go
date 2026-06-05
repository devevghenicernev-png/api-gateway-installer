// Package gendocs implements `apigw gen-docs` — author-side command that
// emits markdown + manpages from the live Cobra command tree.
//
// Hidden because end users have no reason to call it; documentation
// pipelines do. Outputs are deterministic per binary version so CI can
// diff `docs/cli/` to catch undocumented commands.
package gendocs

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdGenDocs(f *cmdutil.Factory) *cobra.Command {
	var (
		mdOut  string
		manOut string
	)
	cmd := &cobra.Command{
		Use:    "gen-docs",
		Short:  "Generate markdown + manpages for the entire command tree",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return run(c.Root(), f, mdOut, manOut)
		},
	}
	cmd.Flags().StringVar(&mdOut, "out", "docs/cli",
		"directory to write per-command markdown")
	cmd.Flags().StringVar(&manOut, "man-out", "",
		"directory to write manpages (skip if empty)")
	return cmd
}

func run(root *cobra.Command, f *cmdutil.Factory, mdOut, manOut string) error {
	// Markdown — one file per command. Cobra adds a "see also" footer; we
	// rewrite the relative link prefix so `mkdocs`/`vitepress` pick them up.
	if mdOut != "" {
		if err := os.MkdirAll(mdOut, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", mdOut, err)
		}
		linkPrefix := func(name string) string { return name }
		// Use the simpler GenMarkdownTree — for our scope the default
		// "see also" links are fine; mkdocs handles relative anchors.
		_ = linkPrefix
		if err := doc.GenMarkdownTree(root, mdOut); err != nil {
			return fmt.Errorf("markdown tree: %w", err)
		}
		fmt.Fprintf(f.IOStreams.Out, "%s wrote markdown to %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			tui.Styles.Identifier.Render(filepath.Clean(mdOut)))
	}

	// Manpages — only when explicitly requested. Sections under 1 by
	// convention for user binaries; goreleaser packs these into deb/rpm.
	if manOut != "" {
		if err := os.MkdirAll(manOut, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", manOut, err)
		}
		hdr := &doc.GenManHeader{
			Title:   "APIGW",
			Section: "1",
			Source:  "apigw",
			Manual:  "API Gateway operator manual",
		}
		if err := doc.GenManTree(root, hdr, manOut); err != nil {
			return fmt.Errorf("man tree: %w", err)
		}
		fmt.Fprintf(f.IOStreams.Out, "%s wrote manpages to %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			tui.Styles.Identifier.Render(filepath.Clean(manOut)))
	}
	return nil
}
