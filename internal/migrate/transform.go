package migrate

import (
	"fmt"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

// Plan is the diff a Transform produces — what would be added, what was
// already present. Empty plan == no-op.
type Plan struct {
	APIsAdded         []config.API
	APIsAlreadyKnown  []string
	DeploysAdded      []config.Deploy
	DeploysAlreadyKnown []string

	// SecretsWritten lists deploy names whose HMAC secret we'll write
	// from the bash payload. We never overwrite a secret that already
	// exists on the new side — secret rotation is an operator action.
	SecretsWritten []string

	// AIRegistered captures bash `type=ai-model` entries — they map onto
	// the same APIs list but get the apigw-ai naming convention applied.
	AIRegistered []string
}

// IsEmpty reports whether the plan would write nothing.
func (p Plan) IsEmpty() bool {
	return len(p.APIsAdded) == 0 && len(p.DeploysAdded) == 0 && len(p.SecretsWritten) == 0
}

// Transform translates bash state into config mutations + secret writes
// without touching disk. Caller applies via Apply().
//
// Idempotent: entries already present in `cfg` are recorded in
// AlreadyKnown rather than appended again. Re-running after a successful
// migration returns an empty plan.
func Transform(cfg *config.Config, apis BashAPIsFile, deploys []BashDeploy) Plan {
	plan := Plan{}

	for _, ba := range apis.APIs {
		if ba.Name == "" || ba.Port == 0 {
			continue
		}
		// AI-model entries — register via the canonical ai-<provider> name
		// so `apigw ai list` finds them. We only do this if the bash name
		// matches a known provider; otherwise treat as a generic API.
		if ba.Type == "ai-model" {
			if p := guessProvider(ba); p != "" {
				name := ai.APIName(p)
				if cfg.FindAPI(name) != nil {
					plan.APIsAlreadyKnown = append(plan.APIsAlreadyKnown, name)
					continue
				}
				plan.APIsAdded = append(plan.APIsAdded, config.API{
					Name:        name,
					Port:        ba.Port,
					Path:        ai.APIPath(p),
					Description: ai.DescriptionPrefix + string(p),
					Enabled:     ba.Enabled,
				})
				plan.AIRegistered = append(plan.AIRegistered, string(p))
				continue
			}
		}
		// Regular API entry. Sanitise the path: bash version sometimes
		// stored it without a leading slash.
		path := ba.Path
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if path == "" {
			path = "/api/" + ba.Name
		}
		if cfg.FindAPI(ba.Name) != nil {
			plan.APIsAlreadyKnown = append(plan.APIsAlreadyKnown, ba.Name)
			continue
		}
		plan.APIsAdded = append(plan.APIsAdded, config.API{
			Name:        ba.Name,
			Port:        ba.Port,
			Path:        path,
			Description: ba.Description,
			Enabled:     ba.Enabled,
		})
	}

	for _, bd := range deploys {
		if bd.ServiceName == "" || bd.GitHubRepo == "" {
			continue
		}
		if cfg.FindDeploy(bd.ServiceName) != nil {
			plan.DeploysAlreadyKnown = append(plan.DeploysAlreadyKnown, bd.ServiceName)
			continue
		}
		runtime := bd.Runtime
		if runtime == "" {
			runtime = "auto"
		}
		branch := bd.Branch
		if branch == "" {
			branch = "main"
		}
		plan.DeploysAdded = append(plan.DeploysAdded, config.Deploy{
			Name:        bd.ServiceName,
			Repo:        bd.GitHubRepo,
			Branch:      branch,
			Port:        bd.Port,
			Path:        "/apps/" + bd.ServiceName,
			Runtime:     runtime,
			Build:       sanitiseCmd(bd.BuildCommand),
			Start:       sanitiseCmd(bd.StartCommand),
			Description: "migrated from bash installer",
			Enabled:     true,
			LastStatus:  "pending",
		})
		if bd.WebhookSecret != "" {
			plan.SecretsWritten = append(plan.SecretsWritten, bd.ServiceName)
		}
	}
	return plan
}

// Apply mutates cfg and writes secret files based on plan. Caller is
// responsible for cfg.Save() and nginx reload.
//
// If a secret already exists on disk we DO NOT overwrite — preserving the
// operator's rotated value. The bash payload is just a fallback.
func Apply(cfg *config.Config, plan Plan, deploys []BashDeploy) error {
	for _, a := range plan.APIsAdded {
		if err := cfg.AddAPI(a); err != nil {
			return fmt.Errorf("add api %s: %w", a.Name, err)
		}
	}
	for _, d := range plan.DeploysAdded {
		if err := cfg.AddDeploy(d); err != nil {
			return fmt.Errorf("add deploy %s: %w", d.Name, err)
		}
	}
	// Secrets: only write for names in plan.SecretsWritten and only if
	// no secret already exists.
	wanted := make(map[string]struct{}, len(plan.SecretsWritten))
	for _, n := range plan.SecretsWritten {
		wanted[n] = struct{}{}
	}
	for _, bd := range deploys {
		if _, ok := wanted[bd.ServiceName]; !ok {
			continue
		}
		if _, err := webhook.LoadSecret(bd.ServiceName); err == nil {
			continue // already present on the new side
		}
		// Write directly via the secret helper isn't available — we use
		// EnsureSecret to create the random one, then overwrite with the
		// bash value. Slight wart; acceptable for the one-shot path.
		// Cleaner alternative: a SetSecret helper. Adding inline below.
		if err := writeSecretRaw(bd.ServiceName, bd.WebhookSecret); err != nil {
			return fmt.Errorf("write secret %s: %w", bd.ServiceName, err)
		}
	}
	return nil
}

// writeSecretRaw persists `value` as the deploy's HMAC secret, atomically.
// We do not expose this as a regular API — it's a migration helper. The
// path mirrors webhook.SecretPath.
func writeSecretRaw(deploy, value string) error {
	// Force a generated secret to materialise the directory + mode, then
	// overwrite content. Saves us from duplicating mkdir/chmod logic.
	if _, err := webhook.EnsureSecret(deploy); err != nil {
		return err
	}
	path := webhook.SecretPath(deploy)
	tmp := path + ".migrate-tmp"
	if err := writeFileMode(tmp, []byte(value), 0o600); err != nil {
		return err
	}
	return renameOver(tmp, path)
}

// guessProvider maps bash ai-model API entries onto our Provider enum.
// The bash installer used names like "ai-ollama" or path /ai/ollama; we
// recognise both shapes here.
func guessProvider(ba BashAPI) ai.Provider {
	for _, candidate := range []string{
		strings.TrimPrefix(ba.Name, "ai-"),
		strings.TrimPrefix(ba.Path, "/ai/"),
	} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if p, ok := ai.ParseProvider(candidate); ok {
			return p
		}
	}
	return ""
}

// sanitiseCmd normalises bash "auto" / "null" sentinels to empty so the
// new deploy engine picks runtime defaults.
func sanitiseCmd(s string) string {
	s = strings.TrimSpace(s)
	if s == "auto" || s == "null" {
		return ""
	}
	return s
}
