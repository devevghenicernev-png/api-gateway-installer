package configcmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// minimalCfg returns a cfg with one API so the export emits at least one path.
func minimalCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.APIs = append(cfg.APIs, config.API{
		Name:    "hello",
		Port:    3000,
		Path:    "/api/hello",
		Enabled: true,
		APIKey: &config.APIKeyAuth{
			Header: "X-Api-Key",
			Keys:   []config.APIKey{{ID: "k1", Secret: "s1"}},
		},
		Lifecycle: &config.Lifecycle{State: "deprecated"},
	})
	return &cfg
}

func TestRenderExport_YAML(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "yaml", "", "", "")
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "name: hello") {
		t.Fatalf("yaml export missing api name: %s", s)
	}
}

func TestRenderExport_JSON(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "json", "", "", "")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("json round-trip: %v\n%s", err, string(body))
	}
	apis, _ := m["apis"].([]any)
	if len(apis) != 1 {
		t.Fatalf("expected 1 api in JSON, got %d (%v)", len(apis), m["apis"])
	}
}

func TestRenderExport_OpenAPI(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "openapi", "My APIs", "v1", "https://example.test")
	if err != nil {
		t.Fatalf("openapi: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "openapi:") || !strings.Contains(s, "3.0.3") {
		t.Fatalf("missing openapi version: %s", s)
	}
	if !strings.Contains(s, "/api/hello") {
		t.Fatalf("missing api path: %s", s)
	}
	if !strings.Contains(s, "apigw_apikey") {
		t.Fatalf("expected apikey security scheme: %s", s)
	}
	if !strings.Contains(s, "deprecated: true") {
		t.Fatalf("deprecated flag should propagate from Lifecycle.State: %s", s)
	}
}

func TestRenderExport_UnknownFormat(t *testing.T) {
	cfg := minimalCfg(t)
	if _, err := renderExport(cfg, "toml", "", "", ""); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestParseImport_YAMLRoundTrip(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "yaml", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	back, err := parseImport(body, "yaml", "in.yaml")
	if err != nil {
		t.Fatalf("import yaml: %v", err)
	}
	if len(back.APIs) != 1 {
		t.Fatalf("api count = %d, want 1", len(back.APIs))
	}
	if back.APIs[0].Name != "hello" {
		t.Fatalf("name = %q, want hello", back.APIs[0].Name)
	}
}

func TestParseImport_JSONRoundTrip(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "json", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	back, err := parseImport(body, "json", "in.json")
	if err != nil {
		t.Fatalf("import json: %v", err)
	}
	if len(back.APIs) != 1 {
		t.Fatalf("api count = %d, want 1 (%v)", len(back.APIs), back.APIs)
	}
	if back.APIs[0].Name != "hello" {
		t.Fatalf("name = %q, want hello", back.APIs[0].Name)
	}
}

func TestParseImport_AutoDetect(t *testing.T) {
	cfg := minimalCfg(t)
	body, err := renderExport(cfg, "json", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	back, err := parseImport(body, "auto", path)
	if err != nil {
		t.Fatalf("auto-detect: %v", err)
	}
	if len(back.APIs) != 1 {
		t.Fatalf("api count = %d, want 1", len(back.APIs))
	}
}

func TestParseImport_GarbageRejected(t *testing.T) {
	if _, err := parseImport([]byte("this is not yaml: ["), "yaml", "junk.yaml"); err == nil {
		t.Fatal("expected error for malformed yaml")
	}
	if _, err := parseImport([]byte(`{`), "json", "junk.json"); err == nil {
		t.Fatal("expected error for malformed json")
	}
}

func TestToOpenAPIList_LifecycleMapping(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "a", Enabled: true},
		{Name: "b", Enabled: true, Lifecycle: &config.Lifecycle{State: "deprecated"}},
		{Name: "c", Enabled: true, Lifecycle: &config.Lifecycle{State: "retired"}},
	}
	got := toOpenAPIList(&cfg)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[1].Deprecated != true || got[1].Retired {
		t.Fatalf("b should be deprecated, got %+v", got[1])
	}
	if got[2].Retired != true {
		t.Fatalf("c should be retired, got %+v", got[2])
	}
}
