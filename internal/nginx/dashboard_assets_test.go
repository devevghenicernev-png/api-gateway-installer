package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestRender_DashboardMount pins the v0.4.4 architecture: the dashboard is
// exposed as a single nginx location at {Dashboard.Path}/, and the gateway
// rewrites the prefix away before proxying. Every dashboard asset —
// HTML, app.css, app.js, favicon.svg, /api/admin/*, /api/status,
// /events — lives under that one mount.
//
// Why this is the right shape (and why the v0.4.1/v0.4.2 design was
// wrong): the gateway namespace at root belongs to user-defined APIs
// (/api/<service>). Exposing /api/admin/* etc. at the root collides
// with user APIs the moment someone names a service "admin" / "status"
// / "logs", and blurs the operator-vs-user security boundary.
//
// Repro before this fix:
//
//	GET /dashboard           → 200 (HTML)
//	GET /app.css             → 200 (worked via the v0.4.1 exact-match
//	                                  location, polluting gateway root)
//	GET /api/admin/audit     → 404 (the bug — dashboard JS calls this,
//	                                  no nginx route exists for it)
//
// After:
//
//	GET /dashboard           → 301 /dashboard/
//	GET /dashboard/          → 200 (HTML, with <base href="/dashboard/">)
//	GET /dashboard/app.css   → 200 (relative <link href="app.css">)
//	GET /dashboard/api/admin/audit → handled by dashboard daemon
func TestRender_DashboardMount(t *testing.T) {
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
		// Bare-path redirect to trailing-slash form.
		"location = /dashboard {",
		"return 301 /dashboard/;",
		// The trailing-slash mount + prefix-strip rewrite.
		"location /dashboard/ {",
		"rewrite ^/dashboard/(.*)$ /$1 break;",
		"proxy_pass http://127.0.0.1:9080;",
		// SSE must not buffer — dashboard's /events streams events live.
		"proxy_buffering off;",
		"X-Accel-Buffering no;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %q\n--- BEGIN ---\n%s\n--- END ---", want, got)
		}
	}

	// The dashboard mount must NOT leak per-asset locations at gateway
	// root any more. Those were the v0.4.1 workaround for the absolute
	// /app.css references in index.html; v0.4.4 fixes the root cause by
	// using <base href="/dashboard/"> + relative URLs.
	for _, mustNotContain := range []string{
		"location = /app.css {",
		"location = /app.js {",
		"location = /favicon.svg {",
	} {
		if strings.Contains(got, mustNotContain) {
			t.Errorf("rendered config still emits %q — should be served via /dashboard/ prefix\n--- BEGIN ---\n%s\n--- END ---",
				mustNotContain, got)
		}
	}
}

// TestRender_DashboardMount_OmittedWhenDisabled ensures the dashboard
// location + 301 redirect are gated on Dashboard.Enabled. With dashboard
// off there's no daemon to proxy to and emitting the block would just
// 502 anything that hits /dashboard/.
func TestRender_DashboardMount_OmittedWhenDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Dashboard.Enabled = false

	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(serverBody)

	for _, mustNotContain := range []string{
		"location = /dashboard {",
		"location /dashboard/ {",
		"return 301 /dashboard/;",
	} {
		if strings.Contains(got, mustNotContain) {
			t.Errorf("rendered config contains %q when dashboard disabled\n--- BEGIN ---\n%s\n--- END ---",
				mustNotContain, got)
		}
	}
}
