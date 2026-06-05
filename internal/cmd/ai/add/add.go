// Package add implements `apigw ai add <provider>`.
package add

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

type options struct {
	f *cmdutil.Factory

	Provider string
	Port     int
	Force    bool // skip detection (use when you know the provider is fine)

	yes    bool
	dryRun bool
}

func NewCmdAdd(f *cmdutil.Factory) *cobra.Command {
	opts := &options{f: f}
	cmd := &cobra.Command{
		Use:   "add <provider>",
		Short: "Register a local LLM provider as a gateway upstream",
		Args:  cobra.ExactArgs(1),
		Example: `  $ apigw ai add ollama
  $ apigw ai add localai --port 8081
  $ apigw ai add vllm --force            # bypass detection (vLLM has no daemon)`,
		RunE: func(c *cobra.Command, args []string) error {
			opts.Provider = args[0]
			opts.yes, _ = c.Flags().GetBool("yes")
			opts.dryRun, _ = c.Flags().GetBool("dry-run")
			return run(opts)
		},
	}
	cmd.Flags().IntVar(&opts.Port, "port", 0, "override the provider's default port")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "register even if the provider isn't detected")
	return cmd
}

func run(opts *options) error {
	p, ok := ai.ParseProvider(opts.Provider)
	if !ok {
		return tui.NewError(
			"unknown provider",
			fmt.Sprintf("got %q, want one of: ollama, localai, vllm", opts.Provider),
		).WithDocs("E_AI_PROVIDER")
	}
	info := p.Info()
	port := opts.Port
	if port == 0 {
		port = info.DefaultPort
	}

	det, err := ai.Detect(p, port)
	if err != nil {
		return err
	}
	if !det.Installed() && !opts.Force {
		return tui.NewError(
			fmt.Sprintf("%s not detected", info.Name),
			fmt.Sprintf("binary %q not on PATH and nothing listening on :%d", info.Binary, port),
		).WithFix(info.InstallHint, "install upstream").
			WithFix("apigw ai add "+string(p)+" --force", "skip detection").
			WithDocs("E_AI_NOT_INSTALLED")
	}

	cfg, err := config.FromFactory(opts.f)
	if err != nil {
		return err
	}

	plan := tui.NewPlan("Register AI provider").
		Add("Provider", info.Name).
		Add("Port", fmt.Sprintf("%d", port)).
		Add("Mount", ai.APIPath(p)).
		Add("Binary", presence(det.BinaryFound, det.BinaryPath)).
		Add("Listening", presence(det.Listening, fmt.Sprintf("127.0.0.1:%d", port)))
	if info.ServiceName != "" {
		plan.Add("Service", systemdLabel(det))
	}
	plan.Render(opts.f.IOStreams.Out, opts.f.IOStreams)

	if opts.dryRun {
		fmt.Fprintln(opts.f.IOStreams.Out, tui.Styles.Muted.Render("dry-run: nothing was changed."))
		return nil
	}

	if !opts.yes && opts.f.IOStreams.IsStdinTTY() {
		ok, err := opts.f.Prompter.Confirm("Apply this plan?", "", true)
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

	if err := ai.Register(cfg, p, port); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := nginx.NewManager().WriteAndReload(cfg); err != nil {
		return tui.NewError("nginx reload failed", err.Error()).
			WithDocs("E_NGINX_RELOAD")
	}

	ios := opts.f.IOStreams
	fmt.Fprintf(ios.Out, "\n%s registered %s on %s\n",
		tui.Styles.Success.Render(tui.GlyphCheck),
		tui.Styles.Identifier.Render(info.Name),
		tui.Styles.Identifier.Render(ai.APIPath(p)))
	fmt.Fprintf(ios.Out, "  %s curl http://localhost%s\n",
		tui.Styles.Accent.Render(tui.GlyphArrow),
		ai.APIPath(p))
	return nil
}

func presence(ok bool, label string) string {
	if !ok {
		return "—"
	}
	return label
}

func systemdLabel(det ai.DetectionResult) string {
	switch {
	case det.Active:
		return "active"
	case det.ServiceFound:
		return "loaded (inactive)"
	default:
		return "—"
	}
}
