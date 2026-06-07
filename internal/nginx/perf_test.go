package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func TestRender_Brotli_OffByDefault(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "brotli ") {
		t.Errorf("brotli leaked while disabled:\n%s", httpBody)
	}
}

func TestRender_Brotli_EnabledWithDefaults(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen.Brotli.Enabled = true
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		"brotli              on;",
		"brotli_comp_level   4;",
		"brotli_min_length   1024;",
		"application/json",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing brotli %q\n%s", want, h)
		}
	}
}

func TestRender_Brotli_CustomLevel(t *testing.T) {
	cfg := config.Defaults()
	cfg.Listen.Brotli = config.Brotli{Enabled: true, Level: 9, MinLength: 512}
	cfg.APIs = []config.API{{Name: "billing", Path: "/b", Enabled: true, Port: 3000}}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, "brotli_comp_level   9;") {
		t.Errorf("custom level not applied:\n%s", h)
	}
	if !strings.Contains(h, "brotli_min_length   512;") {
		t.Errorf("custom min not applied:\n%s", h)
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
