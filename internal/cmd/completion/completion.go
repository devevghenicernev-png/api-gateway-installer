// Package completion implements `apigw completion <shell>` — shell
// completion scripts for bash, zsh, fish, and PowerShell.
//
// Cobra generates the scripts; we wrap them with a friendlier short
// description and per-shell install hints in the Long doc. Output goes to
// stdout so the conventional `apigw completion bash | sudo tee …` pattern
// just works.
package completion

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

func NewCmdCompletion(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "completion <bash|zsh|fish|powershell>",
		Short:                 "Generate shell completion scripts",
		Long:                  longHelp,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		DisableFlagsInUseLine: true,
		RunE: func(c *cobra.Command, args []string) error {
			// Cobra walks up to find the root command via c.Root(); we use
			// stdout (not f.IOStreams.Out) because tee redirection is the
			// canonical install pattern and operators expect "real" stdout.
			out := os.Stdout
			switch args[0] {
			case "bash":
				return c.Root().GenBashCompletionV2(out, true)
			case "zsh":
				return c.Root().GenZshCompletion(out)
			case "fish":
				return c.Root().GenFishCompletion(out, true)
			case "powershell":
				return c.Root().GenPowerShellCompletionWithDesc(out)
			}
			return fmt.Errorf("unsupported shell %q", args[0])
		},
	}
	return cmd
}

const longHelp = `Generate a completion script for the chosen shell.

The script is written to stdout — pipe it into the location your shell
loads completions from. Install paths below are the OS conventions; pick
whichever matches your setup.

Bash:
  # System-wide:
  sudo apigw completion bash > /etc/bash_completion.d/apigw
  # Just you:
  apigw completion bash > ~/.bash_completion.d/apigw

Zsh:
  # If your $fpath does not include /usr/local/share/zsh/site-functions, add it.
  apigw completion zsh | sudo tee /usr/local/share/zsh/site-functions/_apigw

Fish:
  apigw completion fish > ~/.config/fish/completions/apigw.fish

PowerShell:
  apigw completion powershell | Out-String | Invoke-Expression
  # Persist by appending to $PROFILE.

You can also run this any time to refresh after upgrading the binary.`
