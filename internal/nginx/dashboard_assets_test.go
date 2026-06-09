package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestRender_DashboardStaticAssets pins the v0.4.1 fix for the dashboard
// CSS/JS 404 bug: dashboard/index.html references /app.css and /app.js
// with leading slashes; the dashboard prefix location (/dashboard) does
// NOT match those paths so they hit the catch-all `location /` and 404.
// The render must include exact-match locations that proxy them to the
// dashboard daemon.
//
// Repro before fix:
//
//	GET https://<host>/dashboard → 200 (HTML)
//	GET https://<host>/app.css   → 404 (browser console: "Refused to apply style")
//	GET https://<host>/app.js    → 404 (browser console: "Refused to execute script")
func TestRender_DashboardStaticAssets(t *testing.T) {
	cfg := config.Defaults()
	cfg.Dashboard.Enabled = true
	cfg.Dashboard.Port = 9080
	cfg.Dashboard.Path = "/dashboard"

	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(serverBody)

	for _, want := range []string{
		"location = /app.css {",
		"location = /app.js {",
		"proxy_pass http://127.0.0.1:9080;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q\n--- BEGIN config ---\n%s\n--- END config ---", want, got)
		}
	}
}

// TestRender_DashboardStaticAssets_OmittedWhenDisabled ensures we don't
// emit the static-asset locations when the dashboard is off — there's
// no daemon on :9080 to proxy to in that case, and the empty location
// blocks would just return 502.
func TestRender_DashboardStaticAssets_OmittedWhenDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Dashboard.Enabled = false

	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(serverBody)

	for _, unwanted := range []string{
		"location = /app.css",
		"location = /app.js",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("rendered config contains %q when dashboard disabled\n--- BEGIN ---\n%s\n--- END ---", unwanted, got)
		}
	}
}
