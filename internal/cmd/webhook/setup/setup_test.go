package setup

import (
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestPublicHost verifies the resolution order for the printed webhook
// URL host. The literal "<your-host>" is NEVER an acceptable result —
// that's exactly what broke deliveries when operators copied it verbatim
// into GitHub. We always prefer (in order): env override > TLS domain >
// ServerName > routable IP > os.Hostname > "<your-host>" placeholder.
func TestPublicHost(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		tlsDomains []string
		serverName string
		// We don't assert exact IP / hostname — they vary by host. We
		// just assert wantPrecise (string equality) OR wantNotPlaceholder
		// (not the literal "<your-host>"). If wantPrecise is empty, the
		// expectation is "anything but the placeholder".
		wantPrecise string
	}{
		{
			name:        "env override wins over everything",
			env:         "api.example.com",
			tlsDomains:  []string{"tls-domain.example.com"},
			serverName:  "fallback.example.com",
			wantPrecise: "api.example.com",
		},
		{
			name:        "TLS domain when no env",
			tlsDomains:  []string{"tls-domain.example.com"},
			serverName:  "fallback.example.com",
			wantPrecise: "tls-domain.example.com",
		},
		{
			name:        "ServerName when no env or TLS",
			serverName:  "explicit.example.com",
			wantPrecise: "explicit.example.com",
		},
		{
			name:       "ServerName=_ falls back to IP/hostname (NOT placeholder)",
			serverName: "_",
			// Don't assert exact value — depends on host. Just confirm
			// we don't return the literal "<your-host>".
		},
		{
			name: "empty ServerName falls back to IP/hostname (NOT placeholder)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("APIGW_PUBLIC_HOST", tc.env)
			} else {
				t.Setenv("APIGW_PUBLIC_HOST", "")
			}
			cfg := &config.Config{}
			cfg.Listen.ServerName = tc.serverName
			cfg.TLS.Domains = tc.tlsDomains
			got := publicHost(cfg)
			if tc.wantPrecise != "" && got != tc.wantPrecise {
				t.Errorf("publicHost = %q, want %q", got, tc.wantPrecise)
			}
			if got == "<your-host>" {
				t.Errorf("publicHost returned the literal '<your-host>' placeholder — operator would copy this into GitHub verbatim and the webhook would fail DNS")
			}
		})
	}
}
