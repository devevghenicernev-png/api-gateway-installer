// Package enable implements `apigw api enable`.
package enable

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/cobra"
)

func NewCmdEnable(f *cmdutil.Factory) *cobra.Command {
	return newCmd(f, "enable", "Enable an upstream API", true)
}

// newCmd backs both enable.NewCmdEnable and disable.NewCmdDisable — same flow,
// opposite boolean. Kept here so the disable package is one shim.
func newCmd(f *cmdutil.Factory, verb, short string, target bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(f, args[0], target)
		},
	}
}

// Run is exported so disable can call it without duplicating the body.
func Run(f *cmdutil.Factory, name string, enabled bool) error { return run(f, name, enabled) }

func run(f *cmdutil.Factory, name string, enabled bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if cfg.FindAPI(name) == nil {
		return tui.NewError(
			"api not found",
			fmt.Sprintf("no api named %q is registered", name),
		).WithFix("apigw api list", "see what's registered").
			WithDocs("E_API_NOT_FOUND")
	}
	if err := cfg.SetEnabled(name, enabled); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	mgr := nginx.NewManager()
	if err := mgr.WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithFix("apigw doctor", "diagnose nginx state").
			WithDocs("E_NGINX_RELOAD")
	}

	if !f.IOStreams.Quiet() {
		verb := "enabled"
		if !enabled {
			verb = "disabled"
		}
		fmt.Fprintf(f.IOStreams.Out, "%s %s api %s\n",
			tui.Styles.Success.Render(tui.GlyphCheck),
			verb,
			tui.Styles.Identifier.Render(name),
		)
	}
	return nil
}
