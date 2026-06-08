package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func TestRender_Brotli_OffByDefault(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if containsDirective(string(serverBody), "brotli") {
		t.Errorf("brotli leaked while disabled:\n%s", serverBody)
	}
	if containsDirective(string(httpBody), "brotli") {
		t.Errorf("brotli leaked into http-scope:\n%s", httpBody)
	}
}

// containsDirective reports whether the rendered text has an actual nginx
// directive `name [args];` ignoring '#' comment lines. Used to dodge false
// positives when our template comments mention "gzip" / "brotli" by name.
func containsDirective(body, name string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, name+" ") || strings.HasPrefix(trimmed, name+"\t") {
			return true
		}
	}
	return false
}

func TestRender_Brotli_EnabledWithDefaults(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen.Brotli.Enabled = true
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Compression now lives in server scope (BUG-1b: stock Debian/Ubuntu
	// already declares gzip at http scope, so we no longer touch http-scope).
	if containsDirective(string(httpBody), "brotli") {
		t.Errorf("brotli must NOT appear at http-scope (server-scope only):\n%s", httpBody)
	}
	s := string(serverBody)
	for _, want := range []string{
		"brotli              on;",
		"brotli_comp_level   4;",
		"brotli_min_length   1024;",
		"application/json",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing brotli %q\n%s", want, s)
		}
	}
}

func TestRender_Brotli_CustomLevel(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen.Brotli = config.Brotli{Enabled: true, Level: 9, MinLength: 512}
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if !strings.Contains(s, "brotli_comp_level   9;") {
		t.Errorf("custom level not applied:\n%s", s)
	}
	if !strings.Contains(s, "brotli_min_length   512;") {
		t.Errorf("custom min not applied:\n%s", s)
	}
}

// Regression for BUG-1b: gzip must render in server-scope only, never in
// /etc/nginx/conf.d/apigw-http.conf. The stock Debian/Ubuntu nginx.conf
// already has `gzip on;` at http-scope; emitting again duplicates it and
// blows up `nginx -t`.
func TestRender_Gzip_ServerScopeOnly_NotHttpScope(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if containsDirective(string(httpBody), "gzip") {
		t.Errorf("gzip leaked into http-scope (BUG-1b regression):\n%s", httpBody)
	}
	if !strings.Contains(string(serverBody), "gzip                on;") {
		t.Errorf("gzip missing from server-scope:\n%s", serverBody)
	}
}

func TestRender_Gzip_TLSServerScopeOnly(t *testing.T) {
	cfg := config.Defaults()
	cfg.TLS.Strategy = "self-signed"
	cfg.TLS.Domains = []string{"apigw.test"}
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if containsDirective(string(httpBody), "gzip") {
		t.Errorf("gzip leaked into http-scope:\n%s", httpBody)
	}
	if !strings.Contains(string(serverBody), "gzip                on;") {
		t.Errorf("gzip missing from TLS server-scope:\n%s", serverBody)
	}
}

func TestRender_EarlyHints(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Enabled: true, Port: 3000,
		EarlyHints: []string{
			"</static/app.css>; rel=preload; as=style",
			"</static/app.js>; rel=preload; as=script",
		},
	}}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		`add_header Link "</static/app.css>; rel=preload; as=style" always;`,
		`add_header Link "</static/app.js>; rel=preload; as=script" always;`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
}

func TestRender_HTTP2Push(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "billing", Path: "/b", Enabled: true, Port: 3000,
		HTTP2Push: []string{"/static/app.css", "/static/app.js"},
	}}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"http2_push /static/app.css;",
		"http2_push /static/app.js;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
}

func TestRender_NoEarlyHintsNoPush(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if strings.Contains(s, "add_header Link") {
		t.Errorf("Link header leaked without EarlyHints:\n%s", s)
	}
	if strings.Contains(s, "http2_push") {
		t.Errorf("http2_push leaked without HTTP2Push:\n%s", s)
	}
}
