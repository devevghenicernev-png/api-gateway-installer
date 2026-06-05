// Package rotate implements `apigw webhook rotate-secret <deploy>`.
package rotate

import (
	"errors"
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
	"github.com/spf13/cobra"
)

func NewCmdRotate(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate-secret <deploy>",
		Short: "Generate a new HMAC secret (invalidates the old one)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			yes, _ := c.Flags().GetBool("yes")
			return run(f, args[0], yes)
		},
	}
	return cmd
}

func run(f *cmdutil.Factory, name string, yes bool) error {
	cfg, err := config.FromFactory(f)
	if err != nil {
		return err
	}
	if cfg.FindDeploy(name) == nil {
		return tui.NewError("deploy not found", fmt.Sprintf("no deployment named %q", name)).
			WithDocs("E_DEPLOY_NOT_FOUND")
	}

	ios := f.IOStreams
	plan := tui.NewPlan("Rotate webhook secret").
		Add("Deploy", name).
		Add("Effect", "old secret stops working immediately; GitHub deliveries will 401 until updated")
	plan.Render(ios.Out, ios)

	if !yes && ios.IsStdinTTY() {
		ok, err := f.Prompter.Confirm("Apply this plan?", "", false)
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

	secret, err := webhook.RotateSecret(name)
	if err != nil {
		return fmt.Errorf("rotate: %w", err)
	}
	fmt.Fprintln(ios.Out)
	fmt.Fprintln(ios.Out, tui.Styles.Heading.Render("New secret (printed once):"))
	fmt.Fprintln(ios.Out, "  "+tui.Styles.Identifier.Render(secret))
	fmt.Fprintln(ios.Out)
	fmt.Fprintln(ios.Out, tui.Styles.Muted.Render(
		"Update GitHub: github.com/<owner>/<repo>/settings/hooks → Edit → Secret"))
	return nil
}
