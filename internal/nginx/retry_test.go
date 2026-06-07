package nginx

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func retryAPI(r *config.Retry) config.API {
	return config.API{
		Name:    "billing",
		Path:    "/billing",
		Enabled: true,
		Port:    3000,
		Retry:   r,
	}
}

// TestRender_Retry_DefaultsWhenEmpty — an empty Retry block (non-nil
// but no fields set) should enable the safe default condition set and
// stick with the template's default tries=3.
func TestRender_Retry_DefaultsWhenEmpty(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "proxy_next_upstream error timeout http_502 http_503 http_504;") {
		t.Errorf("missing safe default conditions\n%s", s)
	}
	if !strings.Contains(s, "proxy_next_upstream_tries 3;") {
		t.Errorf("missing default tries\n%s", s)
	}
}

// TestRender_Retry_CustomAttempts — explicit Attempts overrides the
// hardcoded template default.
func TestRender_Retry_CustomAttempts(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{
		Conditions: []string{"error", "timeout"},
		Attempts:   5,
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "proxy_next_upstream error timeout;") {
		t.Errorf("missing custom conditions\n%s", s)
	}
	if !strings.Contains(s, "proxy_next_upstream_tries 5;") {
		t.Errorf("custom attempts not applied\n%s", s)
	}
}

// TestRender_Retry_OnStatusMerge — Conditions + OnStatus merge into
// the proxy_next_upstream line, de-duplicated, conditions first.
func TestRender_Retry_OnStatusMerge(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{
		Conditions: []string{"timeout"},
		OnStatus:   []int{502, 503, 504, 408},
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	want := "proxy_next_upstream timeout http_502 http_503 http_504 http_408;"
	if !strings.Contains(s, want) {
		t.Errorf("merge order/format wrong\nwant: %s\n--- emit ---\n%s", want, s)
	}
}

// TestRender_Retry_OnStatusDedup — duplicate http_NNN in both
// Conditions and OnStatus appears only once.
func TestRender_Retry_OnStatusDedup(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{
		Conditions: []string{"http_502", "timeout"},
		OnStatus:   []int{502, 503}, // 502 is a duplicate
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	want := "proxy_next_upstream http_502 timeout http_503;"
	if !strings.Contains(s, want) {
		t.Errorf("dedup failed\nwant: %s\n--- emit ---\n%s", want, s)
	}
}

// TestRender_Retry_PerTry — PerTry sets proxy_read_timeout AND, when
// combined with Attempts, sets proxy_next_upstream_timeout = product.
func TestRender_Retry_PerTry(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{
		Conditions: []string{"error"},
		Attempts:   3,
		PerTry:     5 * time.Second,
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "proxy_read_timeout                 5s;") {
		t.Errorf("missing per-try read timeout\n%s", s)
	}
	if !strings.Contains(s, "proxy_next_upstream_timeout 15s;") {
		t.Errorf("missing total budget (per-try × attempts)\n%s", s)
	}
}

// TestRender_Retry_PerTryWithoutAttempts — PerTry alone sets only the
// per-attempt timeout; no overall budget (operator gets per-attempt
// behaviour they wanted, no surprise total cap).
func TestRender_Retry_PerTryWithoutAttempts(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(&config.Retry{
		Conditions: []string{"error"},
		PerTry:     5 * time.Second,
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "proxy_read_timeout                 5s;") {
		t.Errorf("missing read timeout\n%s", s)
	}
	if strings.Contains(s, "proxy_next_upstream_timeout") {
		t.Errorf("should NOT emit total budget without Attempts\n%s", s)
	}
}

// TestRender_NoRetry — without a Retry block, no proxy_next_upstream
// directives emit (the legacy HealthCheck-driven default still kicks
// in elsewhere — out of scope for this test).
func TestRender_NoRetry(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{retryAPI(nil)}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(serverBody), "proxy_next_upstream ") {
		t.Errorf("no-Retry should not emit proxy_next_upstream:\n%s", serverBody)
	}
}
