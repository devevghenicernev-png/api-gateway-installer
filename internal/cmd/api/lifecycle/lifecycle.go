// Package lifecycle owns `apigw api lifecycle` plus the friendly
// verbs `apigw api publish/deprecate/retire`. Tracks an API through
// draft → published → deprecated → retired, with nginx-emitted
// state-specific responses (503 for draft, RFC 8594 Deprecation +
// Sunset headers for deprecated, 410 Gone for retired).
package lifecycle

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

var validStates = []string{"draft", "published", "deprecated", "retired"}

// NewCmdLifecycle returns the `apigw api lifecycle` group plus
// individual verbs registered alongside it for ergonomics.
func NewCmdLifecycle(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lifecycle <command>",
		Short: "API lifecycle state machine (draft → published → deprecated → retired)",
	}
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newSet(f))
	return cmd
}

// NewVerbCmds returns the standalone `publish`, `deprecate`, `retire`
// verbs (registered at the same level as lifecycle by the api
// subcommand parent). They're aliases for `lifecycle set`.
func NewVerbCmds(f *cmdutil.Factory) []*cobra.Command {
	publish := stateVerb(f, "publish", "published", "Promote an API to published state",
		"  $ apigw api publish billing")
	deprecate := stateVerbWithFlags(f, "deprecate", "deprecated",
		"Flag an API as deprecated (adds RFC 8594 headers)",
		`  $ apigw api deprecate billing --sunset 2026-12-31T23:59:59Z --replacement https://api.example.com/v2`)
	retire := stateVerbWithFlags(f, "retire", "retired",
		"Retire an API — router returns 410 Gone",
		`  $ apigw api retire billing --replacement https://api.example.com/v2`)
	return []*cobra.Command{publish, deprecate, retire}
}

func stateVerb(f *cmdutil.Factory, verb, state, short, example string) *cobra.Command {
	return &cobra.Command{
		Use:     verb + " <api>",
		Short:   short,
		Args:    cobra.ExactArgs(1),
		Example: example,
		RunE: func(c *cobra.Command, args []string) error {
			return applyState(f, args[0], state, time.Time{}, "")
		},
	}
}

func stateVerbWithFlags(f *cmdutil.Factory, verb, state, short, example string) *cobra.Command {
	var sunsetStr, replacement string
	cmd := &cobra.Command{
		Use:     verb + " <api>",
		Short:   short,
		Args:    cobra.ExactArgs(1),
		Example: example,
		RunE: func(c *cobra.Command, args []string) error {
			var sunset time.Time
			if sunsetStr != "" {
				t, err := time.Parse(time.RFC3339, sunsetStr)
				if err != nil {
					return fmt.Errorf("--sunset must be RFC3339 (e.g. 2026-12-31T23:59:59Z): %w", err)
				}
				sunset = t
			}
			return applyState(f, args[0], state, sunset, replacement)
		},
	}
	cmd.Flags().StringVar(&sunsetStr, "sunset", "", "RFC3339 timestamp for the Sunset header (deprecated state only)")
	cmd.Flags().StringVar(&replacement, "replacement", "",
		"successor API URL — sent as `Link: <url>; rel=\"successor-version\"`")
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show the API's lifecycle state",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			out := f.IOStreams.Out
			if api.Lifecycle == nil {
				fmt.Fprintf(out, "%s: no lifecycle set (implicitly published)\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "%s lifecycle:\n", args[0])
			fmt.Fprintf(out, "  state:       %s\n", api.Lifecycle.State)
			fmt.Fprintf(out, "  sunset:      %s\n", dashIfZeroT(api.Lifecycle.SunsetAt))
			fmt.Fprintf(out, "  replacement: %s\n", dashIfEmpty(api.Lifecycle.ReplacementURL))
			return nil
		},
	}
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var state, sunsetStr, replacement string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set the lifecycle state (low-level — prefer publish/deprecate/retire)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !validState(state) {
				return fmt.Errorf("--state must be one of: %v", validStates)
			}
			var sunset time.Time
			if sunsetStr != "" {
				t, err := time.Parse(time.RFC3339, sunsetStr)
				if err != nil {
					return fmt.Errorf("--sunset: %w", err)
				}
				sunset = t
			}
			return applyState(f, args[0], state, sunset, replacement)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "draft | published | deprecated | retired")
	cmd.Flags().StringVar(&sunsetStr, "sunset", "", "RFC3339 timestamp")
	cmd.Flags().StringVar(&replacement, "replacement", "", "successor URL")
	return cmd
}

// ---------- helpers ----------

func applyState(f *cmdutil.Factory, api string, state string, sunset time.Time, replacement string) error {
	cfg, err := loadCfg(f)
	if err != nil {
		return err
	}
	a := cfg.FindAPI(api)
	if a == nil {
		return fmt.Errorf("api %q not found", api)
	}
	a.Lifecycle = &config.Lifecycle{
		State:          state,
		SunsetAt:       sunset.UTC(),
		ReplacementURL: replacement,
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	fmt.Fprintf(f.IOStreams.Out, "%s → %s\n", api, state)
	if !sunset.IsZero() {
		fmt.Fprintf(f.IOStreams.Out, "  sunset:      %s\n", sunset.UTC().Format(time.RFC3339))
	}
	if replacement != "" {
		fmt.Fprintf(f.IOStreams.Out, "  replacement: %s\n", replacement)
	}
	fmt.Fprintln(f.IOStreams.Out, "  run `apigw api reload` to apply.")
	return nil
}

func loadCfg(f *cmdutil.Factory) (*config.Config, error) {
	c, err := f.Config()
	if err != nil {
		return nil, err
	}
	cfg, ok := c.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("config: unexpected type %T", c)
	}
	return cfg, nil
}

func validState(s string) bool {
	for _, v := range validStates {
		if v == s {
			return true
		}
	}
	return false
}

func dashIfZeroT(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
