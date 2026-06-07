package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestRender_BotGuard_IPReputation_OptIn covers the §1.7 IP reputation
// path: the global geo block emits only when at least one API opts
// in via BotGuard.UseIPReputation AND Security.IPReputationFeed is set.
func TestRender_BotGuard_IPReputation_OptIn(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.IPReputationFeed = "/etc/apigw/badlist.conf"
	cfg.APIs = []config.API{
		{
			Name:    "billing",
			Path:    "/billing",
			Enabled: true,
			Port:    3000,
			BotGuard: &config.BotGuard{
				UseIPReputation: true,
			},
		},
	}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		"geo $apigw_ip_blocked {",
		"default 0;",
		"include /etc/apigw/badlist.conf;",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n--- emit ---\n%s", want, h)
		}
	}
	s := string(serverBody)
	if !strings.Contains(s, `if ($apigw_ip_blocked = 1) { return 403; }`) {
		t.Errorf("server config missing IP-block if-check\n--- emit ---\n%s", s)
	}
}

// IP reputation block must NOT emit when the feed is set but no API
// opts in — keeps the http{} config clean for installs that don't use it.
func TestRender_BotGuard_IPReputation_NoOptIn_NoEmit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.IPReputationFeed = "/etc/apigw/badlist.conf"
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/billing", Enabled: true, Port: 3000},
	}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "$apigw_ip_blocked") {
		t.Errorf("geo block emitted with no opt-in:\n%s", httpBody)
	}
}

// Same: opt-in but feed not configured → no emission (would render an
// invalid `include ""` otherwise).
func TestRender_BotGuard_IPReputation_NoFeed_NoEmit(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{
			Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
			BotGuard: &config.BotGuard{UseIPReputation: true},
		},
	}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "$apigw_ip_blocked") {
		t.Errorf("geo block emitted with no feed configured:\n%s", httpBody)
	}
}

// TLS pattern blocking — per-API map at http{} scope + if-check in
// the location.
func TestRender_BotGuard_BlockTLSPatterns(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{
			Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
			BotGuard: &config.BotGuard{
				BlockTLSPatterns: []string{
					"^TLSv1\\.0",
					"^TLSv1\\.1",
				},
			},
		},
	}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	h := string(httpBody)
	for _, want := range []string{
		`map "$ssl_protocol|$ssl_cipher|$ssl_curves|$ssl_ciphers" $apigw_tls_blocked_billing {`,
		`"~^TLSv1\.0" 1;`,
		`"~^TLSv1\.1" 1;`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q\n--- emit ---\n%s", want, h)
		}
	}
	s := string(serverBody)
	if !strings.Contains(s, `if ($apigw_tls_blocked_billing = 1) { return 403; }`) {
		t.Errorf("server config missing TLS-block if-check\n--- emit ---\n%s", s)
	}
}

// TLS pattern map must NOT emit for APIs with no patterns (empty
// list isn't a "block all" — it's "no rule").
func TestRender_BotGuard_NoTLSPatterns_NoEmit(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
			BotGuard: &config.BotGuard{BlockTLSPatterns: nil}},
	}
	_, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(httpBody), "$apigw_tls_blocked_") {
		t.Errorf("map block emitted with no patterns:\n%s", httpBody)
	}
}

// ForwardTLSFingerprint surfaces the synthetic profile to the upstream.
func TestRender_BotGuard_ForwardTLSFingerprint(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{
			Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
			BotGuard: &config.BotGuard{ForwardTLSFingerprint: true},
		},
	}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	want := `proxy_set_header X-Apigw-TLS-Profile "$ssl_protocol/$ssl_cipher/$ssl_curves/$ssl_ciphers";`
	if !strings.Contains(s, want) {
		t.Errorf("server config missing %q\n--- emit ---\n%s", want, s)
	}
}

// Combined: all three new BotGuard features together — they don't
// interfere with each other or the legacy UA-based rules.
func TestRender_BotGuard_AllFeaturesCombined(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.IPReputationFeed = "/etc/apigw/badlist.conf"
	cfg.APIs = []config.API{
		{
			Name: "billing", Path: "/billing", Enabled: true, Port: 3000,
			BotGuard: &config.BotGuard{
				BlockUserAgents:       []string{"BadBot"},
				RequireUserAgent:      true,
				BlockCommonScanners:   true,
				UseIPReputation:       true,
				ForwardTLSFingerprint: true,
				BlockTLSPatterns:      []string{"^TLSv1\\.0"},
			},
		},
	}
	serverBody, httpBody, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		`if ($http_user_agent ~* "BadBot")`,
		`if ($http_user_agent = "")`,
		`if ($http_user_agent ~* "(nikto|nmap|masscan|zgrab|sqlmap|fuzz|curl/[0-7]|wget)")`,
		`if ($apigw_ip_blocked = 1)`,
		`if ($apigw_tls_blocked_billing = 1)`,
		`proxy_set_header X-Apigw-TLS-Profile`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("server config missing %q", want)
		}
	}
	h := string(httpBody)
	for _, want := range []string{
		"geo $apigw_ip_blocked",
		`map "$ssl_protocol|$ssl_cipher|$ssl_curves|$ssl_ciphers" $apigw_tls_blocked_billing`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("http config missing %q", want)
		}
	}
}
