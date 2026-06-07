package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func bluegreenAPI(active string) config.API {
	return config.API{
		Name:    "billing",
		Path:    "/billing",
		Enabled: true,
		// Port is intentionally non-zero — BlueGreen must override it
		// when set, otherwise nginx ends up with two upstream
		// definitions for the same name.
		Port: 3000,
		BlueGreen: &config.BlueGreen{
			Blue:   []config.Upstream{{Address: "10.0.1.10:3000"}, {Address: "10.0.1.11:3000"}},
			Green:  []config.Upstream{{Address: "10.0.2.10:3000"}, {Address: "10.0.2.11:3000"}},
			Active: active,
		},
	}
}

// TestRender_BlueGreen_ActiveBlue — blue pool servers emitted as the
// primary apigw_billing upstream; green pool absent from nginx config.
func TestRender_BlueGreen_ActiveBlue(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bluegreenAPI("blue")}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		"upstream apigw_billing {",
		"server 10.0.1.10:3000",
		"server 10.0.1.11:3000",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing blue server %q\n--- emit ---\n%s", want, h)
		}
	}
	// Green servers must NOT appear in any upstream block.
	for _, leak := range []string{"10.0.2.10:3000", "10.0.2.11:3000"} {
		if strings.Contains(h, leak) {
			t.Errorf("green server %q leaked into config while inactive\n--- emit ---\n%s", leak, h)
		}
	}
}

// TestRender_BlueGreen_ActiveGreen — symmetric to above.
func TestRender_BlueGreen_ActiveGreen(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bluegreenAPI("green")}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{"server 10.0.2.10:3000", "server 10.0.2.11:3000"} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing green server %q\n%s", want, h)
		}
	}
	for _, leak := range []string{"10.0.1.10:3000", "10.0.1.11:3000"} {
		if strings.Contains(h, leak) {
			t.Errorf("blue server %q leaked while inactive\n%s", leak, h)
		}
	}
}

// TestRender_BlueGreen_OverridesPort — when BG is set, the legacy
// API.Port is ignored (otherwise both `server 127.0.0.1:3000` from
// Port AND the pool servers would land in the upstream block).
func TestRender_BlueGreen_OverridesPort(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{bluegreenAPI("blue")}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "127.0.0.1:3000") {
		t.Errorf("legacy port leaked into upstream while BlueGreen is active:\n%s", httpBody)
	}
}

// TestRender_BlueGreen_InvalidActive — Active=other (or unset) →
// ActivePool returns nil → fall back to legacy upstreams (which in
// this test means the API.Port). Misconfiguration shouldn't crash,
// just degrade gracefully.
func TestRender_BlueGreen_InvalidActive(t *testing.T) {
	cfg := config.Defaults()
	api := bluegreenAPI("orange")
	cfg.APIs = []config.API{api}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(httpBody), "127.0.0.1:3000") {
		t.Errorf("invalid Active should fall back to legacy Port, got:\n%s", httpBody)
	}
}

// TestBlueGreen_ActivePool_NilSafe — direct check on the helper.
func TestBlueGreen_ActivePool_NilSafe(t *testing.T) {
	var b *config.BlueGreen
	if got := b.ActivePool(); got != nil {
		t.Errorf("nil receiver: got %v want nil", got)
	}
}

func TestBlueGreen_ActivePool_Modes(t *testing.T) {
	b := &config.BlueGreen{
		Blue:  []config.Upstream{{Address: "blue.local:80"}},
		Green: []config.Upstream{{Address: "green.local:80"}},
	}
	b.Active = "blue"
	if got := b.ActivePool(); len(got) != 1 || got[0].Address != "blue.local:80" {
		t.Errorf("active=blue: got %v", got)
	}
	b.Active = "green"
	if got := b.ActivePool(); len(got) != 1 || got[0].Address != "green.local:80" {
		t.Errorf("active=green: got %v", got)
	}
	b.Active = "other"
	if got := b.ActivePool(); got != nil {
		t.Errorf("active=other: got %v want nil", got)
	}
	b.Active = ""
	if got := b.ActivePool(); got != nil {
		t.Errorf("active=\"\": got %v want nil", got)
	}
}
