// Package ai is the parent of `apigw ai …` — local LLM provider registration.
package ai

import (
	aiAdd "github.com/devevghenicernev-png/apigw/internal/cmd/ai/add"
	aiList "github.com/devevghenicernev-png/apigw/internal/cmd/ai/list"
	aiPull "github.com/devevghenicernev-png/apigw/internal/cmd/ai/pull"
	aiRemove "github.com/devevghenicernev-png/apigw/internal/cmd/ai/remove"
	aiStatus "github.com/devevghenicernev-png/apigw/internal/cmd/ai/status"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdAI(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai <command>",
		Short: "Local LLM providers (Ollama, LocalAI, vLLM)",
		Long: "Register a locally-running LLM provider as an nginx upstream so\n" +
			"clients can call /ai/<provider> directly. apigw does NOT install the\n" +
			"provider — point us at one that's already on the box.",
		Example: `  $ apigw ai add ollama
  $ apigw ai list
  $ apigw ai pull ollama llama3
  $ apigw ai status ollama`,
	}
	cmd.AddCommand(aiAdd.NewCmdAdd(f))
	cmd.AddCommand(aiList.NewCmdList(f))
	cmd.AddCommand(aiRemove.NewCmdRemove(f))
	cmd.AddCommand(aiPull.NewCmdPull(f))
	cmd.AddCommand(aiStatus.NewCmdStatus(f))
	return cmd
}
