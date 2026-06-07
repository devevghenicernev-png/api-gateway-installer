package nginx

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func lifecycleAPI(lc *config.Lifecycle) config.API {
	return config.API{
		Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
		Lifecycle: lc,
	}
}

func TestRender_Lifecycle_Draft(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{lifecycleAPI(&config.Lifecycle{State: "draft"})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), `return 503 "draft API\n";`) {
		t.Errorf("draft missing 503:\n%s", serverBody)
	}
}

func TestRender_Lifecycle_Published(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{lifecycleAPI(&config.Lifecycle{State: "published"})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, leak := range []string{"return 503", "return 410", "Deprecation", "Sunset"} {
		if strings.Contains(s, leak) {
			t.Errorf("published shouldn't emit %q:\n%s", leak, s)
		}
	}
}

func TestRender_Lifecycle_DeprecatedWithSunsetAndReplacement(t *testing.T) {
	sunset := time.Date(2027, 12, 31, 23, 59, 59, 0, time.UTC)
	cfg := config.Defaults()
	cfg.APIs = []config.API{lifecycleAPI(&config.Lifecycle{
		State:          "deprecated",
		SunsetAt:       sunset,
		ReplacementURL: "https://api.example.com/v2/billing",
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		`add_header Deprecation "true" always;`,
		`add_header Sunset "` + sunset.Format(time.RFC1123) + `" always;`,
		`add_header Link "<https://api.example.com/v2/billing>; rel=\"successor-version\"" always;`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
}

func TestRender_Lifecycle_Retired(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{lifecycleAPI(&config.Lifecycle{
		State:          "retired",
		ReplacementURL: "https://api.example.com/v2/billing",
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, `return 410 "retired API\n";`) {
		t.Errorf("retired missing 410:\n%s", s)
	}
	if !strings.Contains(s, `add_header Link "<https://api.example.com/v2/billing>`) {
		t.Errorf("retired missing successor Link:\n%s", s)
	}
}

func TestRender_Lifecycle_RetiredWithoutReplacement(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{lifecycleAPI(&config.Lifecycle{State: "retired"})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "return 410") {
		t.Errorf("still want 410 even without replacement:\n%s", s)
	}
	if strings.Contains(s, "successor-version") {
		t.Errorf("no replacement = no Link:\n%s", s)
	}
}
