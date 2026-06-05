// Package remove implements `apigw ai remove <provider>`.
//
// Removes the nginx upstream registration. Does NOT uninstall the provider
// — apigw never installed it.
package remove

import (
	"errors"
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdRemove(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "remove <provider>",
		Aliases: []string{"rm", "delete"},
		Short:   "Deregister an AI provider from the gateway",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			yes, _ := c.Flags().GetBool("yes")
			return run(f, args[0], yes)
		},
	}
	return cmd
}

func run(f *cmdutil.Factory, providerArg string, yes bool) error {
	p, ok := ai.ParseProvider(providerArg)
	if !ok {
		return tui.NewError("unknown provider", fmt.Sprintf("got %q", providerArg)).
			WithDocs("E_AI_PROVIDER")
	}
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if cfg.FindAPI(ai.APIName(p)) == nil {
		return tui.NewError(
			"provider not registered",
			fmt.Sprintf("%s is not currently exposed by apigw", p),
		).WithFix("apigw ai list", "see what's registered").
			WithDocs("E_AI_NOT_REGISTERED")
	}

	if !yes && f.IOStreams.IsStdinTTY() {
		ok, err := f.Prompter.Confirm(
			fmt.Sprintf("Deregister %s? (the upstream stays running)", p),
			"", false,
		)
		if err != nil {
			if errors.Is(err, cmdutil.CancelError) {
				return cmdutil.CancelError
			}
			return err
		}
		if !ok {
			return cmdutil.SilentError
		}
	}

	if err := ai.Deregister(cfg, p); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}
	fmt.Fprintf(f.IOStreams.Out, "%s deregistered %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(string(p)))
	return nil
}
