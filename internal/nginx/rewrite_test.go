package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func rewriteAPI(rs []config.RewriteRule) config.API {
	return config.API{
		Name:     "billing",
		Path:     "/billing",
		Enabled:  true,
		Port:     3000,
		Rewrites: rs,
	}
}

func TestRender_Rewrite_MultipleRules(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{rewriteAPI([]config.RewriteRule{
		{Match: `^/billing/v1/(.*)$`, Replace: `/billing/v2/$1`, Flag: "last"},
		{Match: `^/billing/old$`, Replace: `https://new.example.com`, Flag: "permanent"},
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		`rewrite ^/billing/v1/(.*)$ /billing/v2/$1 last;`,
		`rewrite ^/billing/old$ https://new.example.com permanent;`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n%s", want, s)
		}
	}
}

func TestRender_Rewrite_DefaultsFlagToLast(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{rewriteAPI([]config.RewriteRule{
		{Match: `^/x$`, Replace: `/y`}, // no Flag
	})}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(serverBody), `rewrite ^/x$ /y last;`) {
		t.Errorf("flag default to last failed:\n%s", serverBody)
	}
}

func TestRender_Rewrite_InvalidFlagRejected(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{rewriteAPI([]config.RewriteRule{
		{Match: `^/x$`, Replace: `/y`, Flag: "weird"},
	})}
	if _, _, err := NewGenerator().Render(&cfg); err == nil {
		t.Fatalf("expected error for invalid flag")
	}
}

func TestRender_Rewrite_MissingMatchOrReplaceRejected(t *testing.T) {
	for _, r := range []config.RewriteRule{
		{Replace: "/y"},
		{Match: "/x"},
	} {
		cfg := config.Defaults()
		cfg.APIs = []config.API{rewriteAPI([]config.RewriteRule{r})}
		if _, _, err := NewGenerator().Render(&cfg); err == nil {
			t.Errorf("expected error for incomplete rule %+v", r)
		}
	}
}

func TestRender_NoRewrites(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{rewriteAPI(nil)}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(serverBody), "rewrite ") {
		t.Errorf("no-Rewrites should not emit rewrite directives:\n%s", serverBody)
	}
}
