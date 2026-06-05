package migrate

import (
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/ai"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func TestTransform_PlainAPI(t *testing.T) {
	cfg := config.Defaults()
	bashAPIs := BashAPIsFile{APIs: []BashAPI{
		{Name: "billing", Path: "/billing", Port: 8081, Description: "svc", Enabled: true, Type: "api"},
	}}
	plan := Transform(&cfg, bashAPIs, nil)
	if len(plan.APIsAdded) != 1 {
		t.Fatalf("expected 1 API added, got %d", len(plan.APIsAdded))
	}
	a := plan.APIsAdded[0]
	if a.Name != "billing" || a.Port != 8081 || a.Path != "/billing" || !a.Enabled {
		t.Fatalf("unexpected API after transform: %+v", a)
	}
}

func TestTransform_AIProviderRouting(t *testing.T) {
	cfg := config.Defaults()
	bashAPIs := BashAPIsFile{APIs: []BashAPI{
		{Name: "ai-ollama", Path: "/ai/ollama", Port: 11434, Enabled: true, Type: "ai-model"},
	}}
	plan := Transform(&cfg, bashAPIs, nil)
	if len(plan.APIsAdded) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(plan.APIsAdded))
	}
	if len(plan.AIRegistered) != 1 || plan.AIRegistered[0] != string(ai.ProviderOllama) {
		t.Fatalf("AIRegistered = %v, want [ollama]", plan.AIRegistered)
	}
	a := plan.APIsAdded[0]
	if a.Name != ai.APIName(ai.ProviderOllama) {
		t.Fatalf("AI entry must use ai-<provider> naming convention, got %q", a.Name)
	}
}

func TestTransform_PathPrefixAdded(t *testing.T) {
	cfg := config.Defaults()
	bashAPIs := BashAPIsFile{APIs: []BashAPI{
		{Name: "x", Path: "x", Port: 1000, Enabled: true},
	}}
	plan := Transform(&cfg, bashAPIs, nil)
	if plan.APIsAdded[0].Path != "/x" {
		t.Fatalf("Path missing leading slash should be normalised; got %q", plan.APIsAdded[0].Path)
	}
}

func TestTransform_Idempotent(t *testing.T) {
	cfg := config.Defaults()
	_ = cfg.AddAPI(config.API{Name: "billing", Port: 8081, Path: "/billing", Enabled: true})
	bashAPIs := BashAPIsFile{APIs: []BashAPI{
		{Name: "billing", Path: "/billing", Port: 8081, Enabled: true},
	}}
	plan := Transform(&cfg, bashAPIs, nil)
	if len(plan.APIsAdded) != 0 {
		t.Fatalf("re-running on already-migrated config should add nothing; got %d", len(plan.APIsAdded))
	}
	if len(plan.APIsAlreadyKnown) != 1 {
		t.Fatalf("expected 1 already-known entry; got %d", len(plan.APIsAlreadyKnown))
	}
}

func TestTransform_DeployBuildAutoStripped(t *testing.T) {
	cfg := config.Defaults()
	deploys := []BashDeploy{
		{ServiceName: "hello", GitHubRepo: "https://example/h", Branch: "", Port: 3000, BuildCommand: "auto", StartCommand: "null"},
	}
	plan := Transform(&cfg, BashAPIsFile{}, deploys)
	if len(plan.DeploysAdded) != 1 {
		t.Fatalf("expected 1 deploy, got %d", len(plan.DeploysAdded))
	}
	d := plan.DeploysAdded[0]
	if d.Build != "" || d.Start != "" {
		t.Fatalf("auto/null sentinels must be normalised to empty; got Build=%q Start=%q", d.Build, d.Start)
	}
	if d.Branch != "main" {
		t.Fatalf("empty branch must default to main; got %q", d.Branch)
	}
}

func TestTransform_DeployWithSecret(t *testing.T) {
	cfg := config.Defaults()
	deploys := []BashDeploy{
		{ServiceName: "hello", GitHubRepo: "https://example/h", Port: 3000, WebhookSecret: "abc123"},
	}
	plan := Transform(&cfg, BashAPIsFile{}, deploys)
	if len(plan.SecretsWritten) != 1 || plan.SecretsWritten[0] != "hello" {
		t.Fatalf("SecretsWritten = %v, want [hello]", plan.SecretsWritten)
	}
}

func TestTransform_DeployNoSecretSkipsWrite(t *testing.T) {
	cfg := config.Defaults()
	deploys := []BashDeploy{
		{ServiceName: "hello", GitHubRepo: "https://example/h", Port: 3000},
	}
	plan := Transform(&cfg, BashAPIsFile{}, deploys)
	if len(plan.SecretsWritten) != 0 {
		t.Fatalf("missing webhook_secret must not be written; got %v", plan.SecretsWritten)
	}
}
