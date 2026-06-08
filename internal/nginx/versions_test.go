package nginx

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// versionAPI builds a minimal API with a Versioning.Versions slice. Keeps
// the test bodies focused on what's being asserted.
func versionAPI(versions []config.APIVersion) config.Config {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name:    "billing",
		Path:    "/api/billing",
		Port:    3000,
		Enabled: true,
		Versioning: &config.Versioning{
			Strategy: "path",
			Versions: versions,
		},
	}}
	return cfg
}

func TestRender_Versions_Active(t *testing.T) {
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", Upstream: "127.0.0.1:3001", State: "active"},
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"location /api/billing/v1/",
		"proxy_pass http://127.0.0.1:3001;",
		`proxy_set_header X-Apigw-Version   "v1";`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestRender_Versions_Deprecated_EmitsSunset(t *testing.T) {
	sunset := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", Upstream: "127.0.0.1:3001", State: "deprecated", SunsetAt: sunset, ReplacementURL: "/api/billing/v2"},
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		`add_header Deprecation "true" always;`,
		`add_header Sunset "`,
		`add_header Link "</api/billing/v2>; rel=\"successor-version\"" always;`,
		"proxy_pass http://127.0.0.1:3001;", // still proxies — deprecated, not retired
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestRender_Versions_Retired_410GoneAndLink(t *testing.T) {
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", State: "retired", ReplacementURL: "/api/billing/v2"},
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, `return 410 "version v1 retired\n";`) {
		t.Errorf("missing 410 for retired version:\n%s", s)
	}
	if !strings.Contains(s, `add_header Link "</api/billing/v2>; rel=\"successor-version\"" always;`) {
		t.Errorf("missing successor-version Link:\n%s", s)
	}
	// Must NOT emit proxy_pass for a retired version (would route past 410).
	idx := strings.Index(s, "location /api/billing/v1/")
	end := strings.Index(s[idx:], "}") + idx
	block := s[idx:end]
	if strings.Contains(block, "proxy_pass") {
		t.Errorf("retired version should NOT have proxy_pass:\n%s", block)
	}
}

func TestRender_Versions_InheritsAPIUpstream_WhenEmpty(t *testing.T) {
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", State: "active"}, // no Upstream → inherit from API.Port=3000
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "proxy_pass http://127.0.0.1:3000;") {
		t.Errorf("v1 should inherit API primary 127.0.0.1:3000:\n%s", serverBody)
	}
}

func TestRender_Versions_PrecedeParentLocation(t *testing.T) {
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", Upstream: "127.0.0.1:3001", State: "active"},
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	v1 := strings.Index(s, "location /api/billing/v1/")
	parent := strings.Index(s, "location /api/billing {")
	if parent < 0 {
		// Some configs render `location /api/billing` without trailing brace
		// when modifier is different. Fall back to a more permissive match.
		parent = strings.Index(s, "location /api/billing")
	}
	if v1 < 0 || parent < 0 {
		t.Fatalf("missing one of expected locations (v1=%d parent=%d):\n%s", v1, parent, s)
	}
	if v1 > parent {
		t.Errorf("version v1 location should appear BEFORE parent /api/billing (v1=%d parent=%d)", v1, parent)
	}
}

func TestRender_Versions_DefaultStateActive(t *testing.T) {
	cfg := versionAPI([]config.APIVersion{
		{Name: "v1", Upstream: "127.0.0.1:3001"}, // empty State
	})
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), "# version: v1 (active)") {
		t.Errorf("empty State should default to active:\n%s", serverBody)
	}
}
