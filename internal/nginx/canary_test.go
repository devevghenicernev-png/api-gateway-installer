package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// canaryAPI is the test fixture: primary on :3000, canary pool of two.
func canaryAPI() config.API {
	return config.API{
		Name:    "billing",
		Path:    "/billing",
		Enabled: true,
		Port:    3000,
		Canary: &config.Canary{
			Weight: 10,
			Upstreams: []config.Upstream{
				{Address: "10.0.1.10:3000"},
				{Address: "10.0.1.11:3000"},
			},
		},
	}
}

// TestRender_Canary_Basic — split_clients + canary upstream + proxy_pass
// uses the split variable directly when no pin is configured.
func TestRender_Canary_Basic(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{canaryAPI()}

	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		`upstream apigw_billing_canary {`,
		`server 10.0.1.10:3000`,
		`server 10.0.1.11:3000`,
		`split_clients "$request_id" $apigw_billing_split {`,
		`10% apigw_billing_canary;`,
		`*                apigw_billing;`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n--- emit ---\n%s", want, h)
		}
	}
	s := string(serverBody)
	// No pin → proxy_pass uses the split variable directly.
	if !strings.Contains(s, `proxy_pass http://$apigw_billing_split;`) {
		t.Errorf("server config doesn't proxy via split variable\n--- emit ---\n%s", s)
	}
}

// TestRender_Canary_Sticky — sticky=true switches hash source to $remote_addr.
func TestRender_Canary_Sticky(t *testing.T) {
	cfg := config.Defaults()
	a := canaryAPI()
	a.Canary.Sticky = true
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, `split_clients "$remote_addr" $apigw_billing_split {`) {
		t.Errorf("sticky should hash on $remote_addr, got:\n%s", h)
	}
	if strings.Contains(h, `split_clients "$request_id"`) {
		t.Errorf("non-sticky split should not emit when sticky=true")
	}
}

// TestRender_Canary_PinHeader — pin map + combined target var.
func TestRender_Canary_PinHeader(t *testing.T) {
	cfg := config.Defaults()
	a := canaryAPI()
	a.Canary.PinHeader = "X-Canary"
	cfg.APIs = []config.API{a}

	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		// Lowercased + dashes→underscores for nginx variable form.
		`map $http_x_canary $apigw_billing_target {`,
		`default $apigw_billing_split;`,
		`"1"     apigw_billing_canary;`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n--- emit ---\n%s", want, h)
		}
	}
	s := string(serverBody)
	if !strings.Contains(s, `proxy_pass http://$apigw_billing_target;`) {
		t.Errorf("pin should route via target var, not split var directly\n--- emit ---\n%s", s)
	}
}

// TestRender_Canary_PinAndSticky — combo: pin map present, hash by IP.
func TestRender_Canary_PinAndSticky(t *testing.T) {
	cfg := config.Defaults()
	a := canaryAPI()
	a.Canary.PinHeader = "X-Canary"
	a.Canary.Sticky = true
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	if !strings.Contains(h, `split_clients "$remote_addr" $apigw_billing_split`) {
		t.Errorf("missing sticky split: %s", h)
	}
	if !strings.Contains(h, `map $http_x_canary $apigw_billing_target`) {
		t.Errorf("missing pin map: %s", h)
	}
}

// TestRender_Canary_PinHeader_Sanitization — header name with dashes
// and uppercase becomes the nginx variable form `$http_x_my_pin`.
func TestRender_Canary_PinHeader_Sanitization(t *testing.T) {
	cfg := config.Defaults()
	a := canaryAPI()
	a.Canary.PinHeader = "X-My-Pin"
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(httpBody), `map $http_x_my_pin $apigw_billing_target`) {
		t.Errorf("header name not sanitised to $http_<lowercase_underscore> form: %s", httpBody)
	}
}

// TestRender_NoCanary — when Canary is absent, nothing canary-related
// emits and proxy_pass goes straight to the primary upstream name.
func TestRender_NoCanary(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/billing", Enabled: true, Port: 3000},
	}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "split_clients") {
		t.Errorf("split_clients leaked without canary:\n%s", httpBody)
	}
	if strings.Contains(string(httpBody), "_canary") {
		t.Errorf("canary upstream leaked without canary block:\n%s", httpBody)
	}
	if !strings.Contains(string(serverBody), `proxy_pass http://apigw_billing;`) {
		t.Errorf("no-canary: should proxy directly to primary upstream\n%s", serverBody)
	}
}

// TestRender_Canary_ZeroWeight — weight=0 means no split emitted at all
// (config block exists but is effectively off).
func TestRender_Canary_ZeroWeight(t *testing.T) {
	cfg := config.Defaults()
	a := canaryAPI()
	a.Canary.Weight = 0
	cfg.APIs = []config.API{a}

	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "split_clients") {
		t.Errorf("zero-weight canary should not emit split_clients:\n%s", httpBody)
	}
}
