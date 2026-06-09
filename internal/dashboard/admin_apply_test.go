package dashboard

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/spf13/afero"
)

// TestAdminAPI_PutTriggersNginxApply pins the v0.4.6 fix for the bug
// surfaced on a real install: PUT /api/admin/apis/<name> updated
// config.yaml (Enabled flipped) but never regenerated the nginx site
// config. Operator clicked Disable in the dashboard, saw "✓ disabled"
// toast, but `curl /api/<name>/...` still returned 200 because nginx
// kept proxying.
//
// We can't easily assert "nginx -t was called" without shelling out,
// but the helpful surrogate is: did the admin handler trigger a
// WriteAndReload (write a fresh site file + invoke reloadCmd)? We
// detect both via the afero memfs + a counter on the reload stub.
func TestAdminAPI_PutTriggersNginxApply(t *testing.T) {
	srv, mux, cfgPath := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})

	// Replace the default Nginx mock from newTestServer with one that
	// counts reload invocations so we can assert the apply path fired.
	fs := afero.NewMemMapFs()
	sitePath := "/etc/nginx/sites-available/apigw.conf"
	var reloads atomic.Int32
	srv.Nginx = nginx.NewManagerWithFS(
		fs,
		sitePath,
		"/etc/nginx/sites-enabled",
		func() error { reloads.Add(1); return nil },
		func(string) error { return nil },
	)

	// Pre-register one API so the PUT has a target.
	body := bytes.NewBufferString(`{"name":"hello","port":3000,"path":"/api/hello","Enabled":true}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed POST: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	// The POST itself must already have triggered an apply.
	if got := reloads.Load(); got != 1 {
		t.Fatalf("POST: expected 1 nginx reload, got %d", got)
	}

	// Now flip Enabled via PUT and assert the second reload happens.
	body = bytes.NewBufferString(`{"name":"hello","port":3000,"path":"/api/hello","Enabled":false}`)
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/apis/hello", body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if got := reloads.Load(); got != 2 {
		t.Fatalf("PUT: expected 2 nginx reloads total, got %d", got)
	}

	// The rendered nginx site file must reflect the new Enabled state
	// — the location block for the disabled API should be gone.
	siteBytes, err := afero.ReadFile(fs, sitePath)
	if err != nil {
		t.Fatalf("read rendered site: %v", err)
	}
	site := string(siteBytes)
	if strings.Contains(site, "location /api/hello") {
		t.Errorf("disabled API still has location block in rendered site:\n%s", site)
	}

	// Sanity: cfgPath was also touched by the existing save path —
	// that's covered by the broader suite; here we only care that the
	// nginx apply happened, which the reload counter + rendered site
	// content above prove.
	_ = cfgPath
}

// TestAdminAPI_DeleteTriggersNginxApply mirrors the PUT case for
// DELETE — removing the API must also strip its nginx location.
func TestAdminAPI_DeleteTriggersNginxApply(t *testing.T) {
	srv, mux, _ := newTestServer(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	})

	fs := afero.NewMemMapFs()
	sitePath := "/etc/nginx/sites-available/apigw.conf"
	var reloads atomic.Int32
	srv.Nginx = nginx.NewManagerWithFS(
		fs,
		sitePath,
		"/etc/nginx/sites-enabled",
		func() error { reloads.Add(1); return nil },
		func(string) error { return nil },
	)

	// Seed an API.
	body := bytes.NewBufferString(`{"name":"hello","port":3000,"path":"/api/hello","Enabled":true}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed POST: %d (%s)", w.Code, w.Body.String())
	}

	// Delete.
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("DELETE", "/api/admin/apis/hello", nil))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: want 204, got %d (%s)", w.Code, w.Body.String())
	}
	if got := reloads.Load(); got != 2 {
		t.Fatalf("DELETE: expected 2 nginx reloads total, got %d", got)
	}

	// Site file should be free of the location for the deleted API.
	siteBytes, err := afero.ReadFile(fs, sitePath)
	if err != nil {
		t.Fatalf("read rendered site: %v", err)
	}
	if strings.Contains(string(siteBytes), "location /api/hello") {
		t.Errorf("deleted API still has location block:\n%s", string(siteBytes))
	}
}
