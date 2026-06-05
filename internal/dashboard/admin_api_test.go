package dashboard

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/events"
)

// newTestServer wires a Server with a temp config file + the optional
// security block. Returns the server, an http handler ready for ServeHTTP,
// and the canonical config path so tests can re-read it after writes.
func newTestServer(t *testing.T, sc config.Security) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Defaults()
	cfg.Security = sc
	cfg.SetPath(cfgPath)
	if err := cfg.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}

	configFn := func() (*config.Config, error) {
		fresh, err := config.LoadFrom(cfgPath)
		if err != nil {
			return nil, err
		}
		return fresh.(*config.Config), nil
	}

	hub := events.New()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv := New(":0", hub, configFn, logger, nil)

	if sc.StateDir == "" {
		sc.StateDir = filepath.Join(dir, "state")
	}
	sec, err := NewSecurity(sc, nil, config.Alerts{}, logger)
	if err != nil {
		t.Fatalf("NewSecurity: %v", err)
	}
	t.Cleanup(sec.Close)
	srv.Sec = sec

	mux := http.NewServeMux()
	srv.Routes(mux)
	return srv, mux, cfgPath
}

// withAuth adds the bearer header for the given token.
func withAuth(r *http.Request, token string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// withCSRF stamps a fresh CSRF token + the bearer in one go.
func withCSRF(t *testing.T, sec *Security, user, tok string, r *http.Request) *http.Request {
	t.Helper()
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-CSRF-Token", sec.IssueCSRFToken(user))
	return r
}

func TestAdminPOSTNoAuthRefused(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "owner-token"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})
	body := bytes.NewBufferString(`{"name":"hello","port":3000}`)
	req := httptest.NewRequest("POST", "/api/admin/apis", body)
	// no auth
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for anon POST, got %d (%s)", w.Code, w.Body.String())
	}
	_ = srv
}

func TestAdminPOSTNoCSRFRefused(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})
	_ = srv
	body := bytes.NewBufferString(`{"name":"hello","port":3000}`)
	req := withAuth(httptest.NewRequest("POST", "/api/admin/apis", body), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for missing CSRF token, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminPOSTHappyPath(t *testing.T) {
	srv, mux, cfgPath := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})

	body := bytes.NewBufferString(`{"name":"hello","port":3000,"enabled":true}`)
	req := httptest.NewRequest("POST", "/api/admin/apis", body)
	req = withCSRF(t, srv.Sec, "root", "tk", req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", w.Code, w.Body.String())
	}

	// Persisted to disk?
	fresh, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	c := fresh.(*config.Config)
	if len(c.APIs) != 1 || c.APIs[0].Name != "hello" {
		t.Fatalf("api not persisted: %+v", c.APIs)
	}

	// Audit must contain the api.add entry.
	if srv.Sec.AuditCount() == 0 {
		t.Fatal("audit was not written")
	}
}

func TestAdminInvalidNameRejected(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})
	body := bytes.NewBufferString(`{"name":"../etc/passwd","port":3000}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid name, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminViewerCantWrite(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "viewer", User: "viewer", Token: "vt"}},
		Assignments: []config.Assignment{{User: "viewer", Roles: []string{"viewer"}}},
	})
	body := bytes.NewBufferString(`{"name":"hello","port":3000}`)
	req := withCSRF(t, srv.Sec, "viewer", "vt", httptest.NewRequest("POST", "/api/admin/apis", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer should not be able to api.add: %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminViewerCanList(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "viewer", User: "viewer", Token: "vt"}},
		Assignments: []config.Assignment{{User: "viewer", Roles: []string{"viewer"}}},
	})
	_ = srv
	req := withAuth(httptest.NewRequest("GET", "/api/admin/apis", nil), "vt")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer GET: %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminDeleteParked(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce:        true,
		ApprovalsThreshold: 2,
		AdminTokens:        []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments:        []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})

	// Seed an API directly via config so DELETE has something to remove.
	cfg, _ := srv.ConfigFn()
	cfg.APIs = append(cfg.APIs, config.API{Name: "hello", Port: 3000})
	if err := cfg.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("DELETE", "/api/admin/apis/hello", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202 parked, got %d (%s)", w.Code, w.Body.String())
	}
	// API must still be there until approvals land.
	cfg2, _ := srv.ConfigFn()
	if len(cfg2.APIs) != 1 {
		t.Fatalf("API was deleted despite parking: %+v", cfg2.APIs)
	}
	// Approval must be persisted.
	pending, _ := srv.Sec.Approvals.List("pending", 10)
	if len(pending) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pending))
	}
}

func TestAdminConfigRedactsSecrets(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{
			{Name: "root", User: "root", Token: "tk"},
			{Name: "ops", User: "ops", Token: "secret-ops-token"},
		},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})
	_ = srv
	req := withAuth(httptest.NewRequest("GET", "/api/admin/config", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("config GET: %d (%s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "secret-ops-token") {
		t.Fatal("config dump leaked an admin token")
	}
	if strings.Contains(body, "tk") && !strings.Contains(body, `"token":"***"`) {
		// `tk` could match as a substring of legit fields; the real check
		// is that tokens were replaced with ***.
		t.Fatal("token field was not redacted to ***")
	}
}

func TestAdminCSRFEndpoint(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
	})
	_ = srv
	req := withAuth(httptest.NewRequest("GET", "/api/admin/csrf", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("csrf endpoint: %d", w.Code)
	}
	var got struct{ Token, User string }
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.User != "root" || got.Token == "" {
		t.Fatalf("unexpected csrf payload: %+v", got)
	}
}

func TestAdminAuditQueryRequiresPerm(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "ops", User: "ops", Token: "ot"}},
		// Operator role can audit.query (per BuiltinRoles).
		Assignments: []config.Assignment{{User: "ops", Roles: []string{"operator"}}},
	})
	_ = srv
	req := withAuth(httptest.NewRequest("GET", "/api/admin/audit", nil), "ot")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("operator should query audit, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminAuditQueryAnonRefused(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
	})
	_ = srv
	req := httptest.NewRequest("GET", "/api/admin/audit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("anon audit must be 403, got %d", w.Code)
	}
}

func TestValidateAPIName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"hello", true},
		{"hello-world_2", true},
		{"", false},
		{"../etc", false},
		{"name with spaces", false},
		{"name;rm -rf /", false},
		{strings.Repeat("a", 65), false},
	} {
		err := validateAPIName(tc.name)
		if tc.ok && err != nil {
			t.Errorf("%q expected valid: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q expected invalid", tc.name)
		}
	}
}
