package nginx

import (
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestRender_JWTEmitsAuthRequest covers F12: when API.JWT is set the
// generator emits the auth_request directive + an internal proxy location
// pointing at the dashboard daemon's /auth/jwt/<api> endpoint.
func TestRender_JWTEmitsAuthRequest(t *testing.T) {
	cfg := config.Defaults()
	cfg.Dashboard.Port = 9080
	cfg.APIs = []config.API{
		{
			Name:    "billing",
			Port:    3000,
			Path:    "/billing",
			Enabled: true,
			JWT: &config.JWT{
				Algorithm: "RS256",
				JWKSURL:   "https://auth.example.com/.well-known/jwks.json",
				Issuer:    "https://auth.example.com/",
				Audience:  "billing",
			},
		},
	}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"auth_request /_apigw_jwt_billing;",
		"location = /_apigw_jwt_billing",
		"proxy_pass http://127.0.0.1:9080/auth/jwt/billing;",
		"proxy_set_header Authorization $http_authorization;",
		"auth_request_set $apigw_subject $upstream_http_x_apigw_subject;",
		"proxy_set_header X-Remote-User $apigw_subject;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n--- emit ---\n%s", want, s)
		}
	}
}

// TestRender_CustomLocation covers F14: CustomLocation passes raw directives
// into the location body verbatim.
func TestRender_CustomLocation(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{
			Name:           "metrics",
			Port:           3000,
			Path:           "/metrics",
			Enabled:        true,
			CustomLocation: "access_log /var/log/nginx/metrics.log custom;\nproxy_intercept_errors on;",
		},
	}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	for _, want := range []string{
		"# apigw custom_location (metrics)",
		"access_log /var/log/nginx/metrics.log custom;",
		"proxy_intercept_errors on;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q\n--- emit ---\n%s", want, s)
		}
	}
}

// TestRender_NoJWTNoAuthRequest negative case — without JWT config, no auth
// directives should appear.
func TestRender_NoJWTNoAuthRequest(t *testing.T) {
	cfg := config.Defaults()
	cfg.APIs = []config.API{
		{Name: "plain", Port: 3000, Path: "/plain", Enabled: true},
	}
	serverBody, _, err := NewGenerator().Render(&cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(serverBody)
	if strings.Contains(s, "auth_request /_apigw_jwt_") {
		t.Errorf("plain API should NOT emit auth_request; got:\n%s", s)
	}
}
