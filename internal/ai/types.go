// Package ai wires local LLM runtimes (Ollama, LocalAI, vLLM) into the
// apigw gateway. We do not reinstall the provider — apigw's job is to
// detect, register, and expose. The bash version reinstalled aggressively
// and produced reams of broken Python venvs; this rewrite is opinionated
// the other way.
//
// Surface:
//
//	apigw ai add <provider>     - detect + register as nginx upstream
//	apigw ai list               - registered providers, with status
//	apigw ai remove <provider>  - deregister, keep the local install
//	apigw ai pull <provider> <model>
//	apigw ai status [provider]
package ai

import "strings"

// Provider enumerates the LLM runtimes apigw natively recognises.
//
// New providers are a one-line addition here plus a Catalog entry below.
// The three we ship cover the bulk of what hobbyists and small-team
// installs on Pi-class hardware actually run.
type Provider string

const (
	ProviderOllama  Provider = "ollama"
	ProviderLocalAI Provider = "localai"
	ProviderVLLM    Provider = "vllm"
)

// All returns the supported providers in display order. Used by `apigw ai
// add` help and shell-completion.
func All() []Provider {
	return []Provider{ProviderOllama, ProviderLocalAI, ProviderVLLM}
}

// IsValid reports whether `p` names a provider apigw knows about.
func (p Provider) IsValid() bool {
	switch p {
	case ProviderOllama, ProviderLocalAI, ProviderVLLM:
		return true
	}
	return false
}

// ProviderInfo is everything we need to detect, install-hint, and proxy
// a given provider. Stored statically in Catalog — runtime state lives in
// the APIs list (naming convention: "ai-<provider>").
type ProviderInfo struct {
	// Name is the human-readable label (e.g. "Ollama").
	Name string

	// DefaultPort is what the provider listens on out of the box.
	DefaultPort int

	// Binary is the executable name we probe on $PATH. Empty for providers
	// that don't ship one (e.g. vLLM is a Python package, no entrypoint).
	Binary string

	// InstallURL is the upstream installer page. Surfaced in error messages
	// when we detect the provider isn't installed.
	InstallURL string

	// InstallHint is a one-line command the user can run to install.
	InstallHint string

	// ServiceName is the canonical systemd unit name, if any. Used by
	// `apigw ai status` to surface health.
	ServiceName string

	// PullArgs is the argv prefix for `apigw ai pull`. For Ollama:
	// `ollama pull <model>`. For providers that don't pull (vLLM), nil.
	PullArgs []string
}

// Catalog is the static set of provider definitions. Indexed by Provider.
//
// Defaults align with each upstream's defaults — we do not invent ports.
var Catalog = map[Provider]ProviderInfo{
	ProviderOllama: {
		Name:        "Ollama",
		DefaultPort: 11434,
		Binary:      "ollama",
		InstallURL:  "https://ollama.com/download",
		InstallHint: "curl -fsSL https://ollama.com/install.sh | sh",
		ServiceName: "ollama.service",
		PullArgs:    []string{"ollama", "pull"}, // append model name
	},
	ProviderLocalAI: {
		Name:        "LocalAI",
		DefaultPort: 8080,
		Binary:      "local-ai",
		InstallURL:  "https://localai.io/basics/getting_started/",
		InstallHint: "curl https://localai.io/install.sh | sh",
		ServiceName: "local-ai.service",
		// LocalAI pulls via HTTP API, not CLI. The pull command handles
		// that branch explicitly instead of shelling out.
		PullArgs: nil,
	},
	ProviderVLLM: {
		Name:        "vLLM",
		DefaultPort: 8000,
		Binary:      "vllm",
		InstallURL:  "https://docs.vllm.ai/en/latest/getting_started/installation.html",
		InstallHint: "pip install vllm",
		ServiceName: "", // typically run as a subprocess, not a unit
		PullArgs:    nil, // vLLM downloads on first inference; no pull step
	},
}

// Info returns Catalog[p]. Defined as a method so callers can chain.
func (p Provider) Info() ProviderInfo { return Catalog[p] }

// APIName is the canonical config.API.Name for a registered AI provider.
//
// "ai-ollama" → ProviderOllama. Naming convention saves us a new struct;
// see register.go for IsAI / ProviderOf.
func APIName(p Provider) string { return "ai-" + string(p) }

// APIPath is the nginx mount path. /ai/<provider> is the canonical URL.
func APIPath(p Provider) string { return "/ai/" + string(p) }

// ParseProvider returns the Provider for `s` (case-insensitive) or empty
// + false if unknown.
func ParseProvider(s string) (Provider, bool) {
	p := Provider(strings.ToLower(strings.TrimSpace(s)))
	return p, p.IsValid()
}
