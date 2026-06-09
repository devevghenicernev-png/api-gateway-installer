package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// TestAdminV1Alias verifies that every /api/admin/* path is ALSO mounted
// at /api/v1/admin/*, so external clients can pin to the versioned
// surface. The contract: hitting /api/v1/admin/<same-path> reaches the
// SAME handler as /api/admin/<same-path>, evidenced by an identical
// status code and response body.
//
// We can't assert a specific status here because most handlers gate
// behind auth/CSRF (401/403), and a few (SSO) intentionally return 404
// when not configured. Both of those are correct behavior and "same
// status on both paths" still proves the alias resolves.
//
// A regression would show up as v1 returning 404 from the mux while
// legacy returns something else — i.e. status mismatch.
func TestAdminV1Alias(t *testing.T) {
	_, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})

	cases := []string{
		"/api/admin/csrf",
		"/api/admin/apis",
		"/api/admin/deploys",
		"/api/admin/streams",
		"/api/admin/consumers",
		"/api/admin/sessions",
		"/api/admin/tenants",
		"/api/admin/audit",
		"/api/admin/approvals",
		"/api/admin/config",
		"/api/admin/tls",
		"/api/admin/gitops",
		"/api/admin/sso/login",
		"/api/admin/sso/callback",
		"/api/admin/alerts/test",
	}

	for _, legacy := range cases {
		t.Run(legacy, func(t *testing.T) {
			v1 := strings.Replace(legacy, "/api/admin/", "/api/v1/admin/", 1)

			rrLegacy := httptest.NewRecorder()
			mux.ServeHTTP(rrLegacy, httptest.NewRequest(http.MethodGet, legacy, nil))

			rrV1 := httptest.NewRecorder()
			mux.ServeHTTP(rrV1, httptest.NewRequest(http.MethodGet, v1, nil))

			// Same handler on both → same status + same body. A
			// difference here means the v1 path resolved to a
			// different handler (or to the mux's default 404 because
			// the alias wasn't registered).
			if rrLegacy.Code != rrV1.Code {
				t.Fatalf("status mismatch: %s=%d  vs  %s=%d (bodies: %q / %q)",
					legacy, rrLegacy.Code, v1, rrV1.Code,
					rrLegacy.Body.String(), rrV1.Body.String())
			}
			if rrLegacy.Body.String() != rrV1.Body.String() {
				t.Fatalf("body mismatch: %s=%q  vs  %s=%q",
					legacy, rrLegacy.Body.String(), v1, rrV1.Body.String())
			}
		})
	}
}

// TestRegisterAdminPanicsOnBadPrefix protects the helper's invariant:
// passing anything that doesn't start with /api/admin is a programmer
// bug, not a runtime situation — panic at startup is the right thing.
func TestRegisterAdminPanicsOnBadPrefix(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for non-/api/admin path, got none")
		}
	}()
	mux := http.NewServeMux()
	registerAdmin(mux, "/something/else", func(http.ResponseWriter, *http.Request) {})
}
