package ai

import (
	"fmt"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// DescriptionPrefix tags an API entry as AI-managed. We use a prefix in the
// existing Description field instead of adding a column to config.API —
// AI providers are just APIs with a known shape, and that lets nginx
// generation, apigw doctor, and apigw backup all keep working unchanged.
const DescriptionPrefix = "managed by apigw ai · "

// IsAI reports whether `a` was registered via `apigw ai add`.
//
// Two checks: the name follows our "ai-<provider>" convention OR the
// description starts with the tag. Either path is sufficient — the second
// catches manually-renamed entries.
func IsAI(a config.API) bool {
	if strings.HasPrefix(a.Name, "ai-") {
		return true
	}
	return strings.HasPrefix(a.Description, DescriptionPrefix)
}

// ProviderOf returns the Provider an AI-managed API points at, or empty if
// the entry isn't AI-managed or the provider tag is unrecognised.
func ProviderOf(a config.API) Provider {
	if !IsAI(a) {
		return ""
	}
	if strings.HasPrefix(a.Name, "ai-") {
		if p, ok := ParseProvider(strings.TrimPrefix(a.Name, "ai-")); ok {
			return p
		}
	}
	tag := strings.TrimPrefix(a.Description, DescriptionPrefix)
	if p, ok := ParseProvider(tag); ok {
		return p
	}
	return ""
}

// ListAI filters cfg.APIs to AI-managed entries.
func ListAI(cfg *config.Config) []config.API {
	out := make([]config.API, 0)
	for _, a := range cfg.APIs {
		if IsAI(a) {
			out = append(out, a)
		}
	}
	return out
}

// Register adds the provider as a config.API. Idempotent — registering twice
// updates the existing entry in place (port can change if the upstream binds
// somewhere non-default).
//
// Caller is responsible for cfg.Save() and nginx reload.
func Register(cfg *config.Config, p Provider, port int) error {
	info, ok := Catalog[p]
	if !ok {
		return fmt.Errorf("unknown provider %q", p)
	}
	if port == 0 {
		port = info.DefaultPort
	}
	a := config.API{
		Name:        APIName(p),
		Port:        port,
		Path:        APIPath(p),
		Description: DescriptionPrefix + string(p),
		Enabled:     true,
	}
	if existing := cfg.FindAPI(a.Name); existing != nil {
		existing.Port = a.Port
		existing.Path = a.Path
		existing.Description = a.Description
		existing.Enabled = true
		return nil
	}
	return cfg.AddAPI(a)
}

// Deregister removes the provider's API entry. Returns nil if no entry was
// registered — Deregister is idempotent like Register.
func Deregister(cfg *config.Config, p Provider) error {
	name := APIName(p)
	if cfg.FindAPI(name) == nil {
		return nil
	}
	return cfg.RemoveAPI(name)
}
