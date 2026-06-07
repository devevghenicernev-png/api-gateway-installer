package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func threeVariantAPI() config.API {
	return config.API{
		Name:    "billing",
		Path:    "/billing",
		Enabled: true,
		Port:    3000,
		Variants: []config.Variant{
			{Name: "v1", Weight: 70, Upstreams: []config.Upstream{{Address: "10.0.1.10:3000"}}},
			{Name: "v2", Weight: 20, Upstreams: []config.Upstream{{Address: "10.0.2.10:3000"}}},
			{Name: "v3", Weight: 10, Upstreams: []config.Upstream{{Address: "10.0.3.10:3000"}}},
		},
	}
}

// TestRender_Variants_ThreeWaySplit — one split_clients with three
// targets; last variant gets `*` so rounding leftovers don't drop.
func TestRender_Variants_ThreeWaySplit(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{threeVariantAPI()}

	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)

	// Each variant gets its own upstream block.
	for _, want := range []string{
		"upstream apigw_billing_v1 {",
		"upstream apigw_billing_v2 {",
		"upstream apigw_billing_v3 {",
		"server 10.0.1.10:3000",
		"server 10.0.2.10:3000",
		"server 10.0.3.10:3000",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n%s", want, h)
		}
	}

	// Single split_clients with three branches; last is wildcard.
	if !strings.Contains(h, `split_clients "$request_id" $apigw_billing_variant`) {
		t.Errorf("split_clients var name unexpected\n%s", h)
	}
	for _, want := range []string{
		"70% apigw_billing_v1;",
		"20% apigw_billing_v2;",
		"* apigw_billing_v3;",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("split branch missing %q\n%s", want, h)
		}
	}

	// proxy_pass routes through the split variable.
	s := string(serverBody)
	if !strings.Contains(s, `proxy_pass http://$apigw_billing_variant;`) {
		t.Errorf("location doesn't proxy via variant variable\n%s", s)
	}
	// Legacy single-port primary upstream should NOT appear when
	// Variants drives the routing (no apigw_billing block).
	if strings.Contains(h, "upstream apigw_billing {") {
		t.Errorf("legacy primary upstream leaked alongside variants\n%s", h)
	}
}

// TestRender_Variants_LastIsWildcard — independent of weight order,
// the LAST variant in the slice is the wildcard branch.
func TestRender_Variants_LastIsWildcard(t *testing.T) {
	cfg := config.Defaults()
	a := config.API{
		Name: "xx", Path: "/x", Enabled: true, Port: 3000,
		Variants: []config.Variant{
			{Name: "small", Weight: 5, Upstreams: []config.Upstream{{Address: "s:1"}}},
			{Name: "big", Weight: 90, Upstreams: []config.Upstream{{Address: "b:1"}}},
		},
	}
	cfg.APIs = []config.API{a}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, "5% apigw_xx_small;") {
		t.Errorf("expected small as the 5%% branch:\n%s", h)
	}
	if !strings.Contains(h, "* apigw_xx_big;") {
		t.Errorf("expected big as the wildcard branch (last declared wins):\n%s", h)
	}
}

// TestRender_Variants_ZeroWeightDropped — variants with weight=0 or
// empty pools are silently skipped.
func TestRender_Variants_ZeroWeightDropped(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "xx", Path: "/x", Enabled: true, Port: 3000,
		Variants: []config.Variant{
			{Name: "a", Weight: 50, Upstreams: []config.Upstream{{Address: "a:1"}}},
			{Name: "b", Weight: 0, Upstreams: []config.Upstream{{Address: "b:1"}}},
			{Name: "c", Weight: 50, Upstreams: []config.Upstream{{Address: "c:1"}}},
		},
	}}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if strings.Contains(h, "apigw_xx_b ") || strings.Contains(h, "apigw_xx_b;") {
		t.Errorf("variant b should be dropped (weight=0):\n%s", h)
	}
	if !strings.Contains(h, "upstream apigw_xx_a") || !strings.Contains(h, "upstream apigw_xx_c") {
		t.Errorf("variants a + c should both render:\n%s", h)
	}
}

// TestRender_Variants_SingletonFallback — one variant isn't a split,
// the generator should fall back to the primary upstream silently
// (no split_clients block emitted, primary upstream still works).
func TestRender_Variants_SingletonFallback(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{
		Name: "xx", Path: "/x", Enabled: true, Port: 3000,
		Variants: []config.Variant{
			{Name: "only", Weight: 100, Upstreams: []config.Upstream{{Address: "o:1"}}},
		},
	}}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "$apigw_xx_variant") {
		t.Errorf("singleton variant should not emit split:\n%s", httpBody)
	}
	if !strings.Contains(string(serverBody), `proxy_pass http://apigw_xx;`) {
		t.Errorf("expected fallback to primary upstream:\n%s", serverBody)
	}
}

// TestRender_Variants_PrecedenceOverCanary — when both are set,
// Variants wins (the generator picks Variants > BlueGreen > Canary).
func TestRender_Variants_PrecedenceOverCanary(t *testing.T) {
	cfg := config.Defaults()
	a := threeVariantAPI()
	a.Canary = &config.Canary{
		Weight:    50,
		Upstreams: []config.Upstream{{Address: "canary-pool:80"}},
	}
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, "$apigw_billing_variant") {
		t.Errorf("Variants should win over Canary:\n%s", h)
	}
	if strings.Contains(h, "canary-pool:80") {
		t.Errorf("Canary upstream should NOT emit when Variants overrides:\n%s", h)
	}
}

// TestRender_NoVariants — clean path: nothing variant-related leaks.
func TestRender_NoVariants(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{{Name: "yy", Path: "/y", Enabled: true, Port: 3000}}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "_variant") {
		t.Errorf("no variants set, but `_variant` appears in config:\n%s", httpBody)
	}
}
