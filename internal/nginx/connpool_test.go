package nginx

import (
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func connPoolAPI(p *config.ConnectionPool) config.API {
	return config.API{
		Name:    "billing",
		Path:    "/billing",
		Enabled: true,
		Port:    3000,
		Upstreams: []config.Upstream{
			{Address: "10.0.0.1:3000"},
			{Address: "10.0.0.2:3000"},
		},
		ConnectionPool: p,
	}
}

// TestRender_ConnPool_DefaultsWhenAbsent — without overrides the
// legacy keepalive=16 is emitted and no timeout/requests directives.
func TestRender_ConnPool_DefaultsWhenAbsent(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{connPoolAPI(nil)}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, "keepalive 16;") {
		t.Errorf("missing legacy default keepalive\n%s", h)
	}
	for _, leak := range []string{"keepalive_timeout", "keepalive_requests"} {
		if strings.Contains(h, leak) {
			t.Errorf("%q leaked without ConnectionPool overrides:\n%s", leak, h)
		}
	}
}

// TestRender_ConnPool_CustomConns — KeepaliveConns replaces the
// default; timeout/requests still absent.
func TestRender_ConnPool_CustomConns(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{connPoolAPI(&config.ConnectionPool{KeepaliveConns: 64})}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, "keepalive 64;") {
		t.Errorf("custom conns not applied:\n%s", h)
	}
	if strings.Contains(h, "keepalive 16;") {
		t.Errorf("legacy default leaked alongside override:\n%s", h)
	}
}

// TestRender_ConnPool_CustomTimeout — KeepaliveTimeout emits
// `keepalive_timeout` with second-precision.
func TestRender_ConnPool_CustomTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{connPoolAPI(&config.ConnectionPool{
		KeepaliveTimeout: 30 * time.Second,
	})}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(httpBody), "keepalive_timeout 30s;") {
		t.Errorf("missing keepalive_timeout:\n%s", httpBody)
	}
}

// TestRender_ConnPool_CustomRequests — KeepaliveRequests emits
// `keepalive_requests`.
func TestRender_ConnPool_CustomRequests(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{connPoolAPI(&config.ConnectionPool{
		KeepaliveRequests: 5000,
	})}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(httpBody), "keepalive_requests 5000;") {
		t.Errorf("missing keepalive_requests:\n%s", httpBody)
	}
}

// TestRender_ConnPool_AllThree — full combo emits all three lines.
func TestRender_ConnPool_AllThree(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{connPoolAPI(&config.ConnectionPool{
		KeepaliveConns:    128,
		KeepaliveTimeout:  45 * time.Second,
		KeepaliveRequests: 2000,
	})}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		"keepalive 128;",
		"keepalive_timeout 45s;",
		"keepalive_requests 2000;",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
}

// TestRender_ConnPool_AppliedToCanaryPool — ConnectionPool overrides
// flow into the canary upstream block too (a 2nd pool deserves the
// same tuning as the primary).
func TestRender_ConnPool_AppliedToCanaryPool(t *testing.T) {
	cfg := config.Defaults()
	a := connPoolAPI(&config.ConnectionPool{KeepaliveConns: 32})
	a.Canary = &config.Canary{
		Weight:    10,
		Upstreams: []config.Upstream{{Address: "canary.local:3000"}},
	}
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	// Two upstream blocks: apigw_billing + apigw_billing_canary.
	// Both should carry the bumped keepalive value.
	if cnt := strings.Count(h, "keepalive 32;"); cnt != 2 {
		t.Errorf("expected keepalive=32 in both upstream blocks, got %d:\n%s", cnt, h)
	}
}
