// Package pull implements `apigw ai pull <provider> <model>`.
package pull

import (
	"context"
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdPull(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <provider> <model>",
		Short: "Download a model into the local provider's cache",
		Args:  cobra.ExactArgs(2),
		Example: `  $ apigw ai pull ollama llama3
  $ apigw ai pull ollama mistral:7b
  $ apigw ai pull localai gpt4all-j`,
		RunE: func(c *cobra.Command, args []string) error {
			return run(c.Context(), f, args[0], args[1])
		},
	}
	return cmd
}

func run(ctx context.Context, f *cmdutil.Factory, providerArg, model string) error {
	p, ok := ai.ParseProvider(providerArg)
	if !ok {
		return tui.NewError("unknown provider", fmt.Sprintf("got %q", providerArg)).
			WithDocs("E_AI_PROVIDER")
	}

	ios := f.IOStreams
	// Plan card per rule 3 — pulling can take 10+ minutes and consume
	// many GB. Users should see what they're about to do before it starts.
	plan := tui.NewPlan("AI model pull").
		Add("Provider", string(p)).
		Add("Model", model).
		Add("Effect", "downloads to provider's local cache (may be 1-50 GB)")
	plan.Render(ios.Out, ios)

	fmt.Fprintf(ios.Out, "\n%s pulling %s for %s …\n",
		tui.Styles.Muted.Render("›"),
		tui.Styles.Identifier.Render(model),
		tui.Styles.Identifier.Render(string(p)))

	if err := ai.Pull(ctx, p, model, ios.Out); err != nil {
		return tui.NewError("pull failed", err.Error()).
			WithFix(fmt.Sprintf("apigw ai status %s", p), "verify the provider is up").
			WithDocs("E_AI_PULL")
	}
	fmt.Fprintf(ios.Out, "%s pulled %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(model))
	return nil
}
