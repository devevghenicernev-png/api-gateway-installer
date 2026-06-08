package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// ===== ETag / If-Match on /api/admin/apis/<name> =====

func TestAdminAPI_ETagOnGet(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mustCreate(t, srv, mux, `{"name":"hello","port":3000,"enabled":true}`)

	req := withAuth(httptest.NewRequest("GET", "/api/admin/apis/hello", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", w.Code)
	}
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("ETag header missing on GET")
	}
}

func TestAdminAPI_IfMatchMismatch_412(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mustCreate(t, srv, mux, `{"name":"hello","port":3000,"enabled":true}`)

	body := bytes.NewBufferString(`{"name":"hello","port":3001,"enabled":true}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/apis/hello", body))
	req.Header.Set("If-Match", `"deadbeefdeadbeef"`) // stale
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-Match mismatch: want 412, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminAPI_IfMatchOK_RoundTrips(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mustCreate(t, srv, mux, `{"name":"hello","port":3000,"enabled":true}`)

	// Fetch current etag.
	req := withAuth(httptest.NewRequest("GET", "/api/admin/apis/hello", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("ETag missing")
	}

	// PUT with matching If-Match → 200.
	body := bytes.NewBufferString(`{"name":"hello","port":3001,"enabled":true}`)
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/apis/hello", body))
	req.Header.Set("If-Match", tag)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT with valid If-Match: want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// ===== Pagination on /api/admin/apis =====

func TestAdminAPIs_PaginationHeaders(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	for i := 0; i < 5; i++ {
		mustCreate(t, srv, mux, `{"name":"a`+strconv.Itoa(i)+`","port":`+strconv.Itoa(3000+i)+`,"enabled":true}`)
	}

	req := withAuth(httptest.NewRequest("GET", "/api/admin/apis?limit=2&offset=1", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Total-Count"); got != "5" {
		t.Fatalf("X-Total-Count = %q, want 5", got)
	}
	if got := w.Header().Get("X-Limit"); got != "2" {
		t.Fatalf("X-Limit = %q, want 2", got)
	}
	var items []config.API
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len items = %d, want 2", len(items))
	}
}

// ===== GitOps singleton =====

func TestAdminGitOps_PUTGetRoundTrip(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())

	body := bytes.NewBufferString(`{"RepoURL":"https://github.com/me/repo","Branch":"main","Path":"config","IntervalSec":60}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/gitops", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	req = withAuth(httptest.NewRequest("GET", "/api/admin/gitops", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", w.Code)
	}
	var got config.GitOps
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RepoURL != "https://github.com/me/repo" {
		t.Fatalf("RepoURL = %q, want round-trip", got.RepoURL)
	}
}

// ===== APIKeys nested CRUD =====

func TestAdminAPIKeys_CreateListDelete(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mustCreate(t, srv, mux, `{"name":"hello","port":3000,"enabled":true}`)

	// POST — create key (server-mints secret since none given).
	body := bytes.NewBufferString(`{"id":"k1","owner":"alice","scopes":["read"]}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis/hello/keys", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	var created config.APIKey
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" {
		t.Fatal("server-minted secret should be returned on create")
	}

	// GET list — secret redacted.
	req = withAuth(httptest.NewRequest("GET", "/api/admin/apis/hello/keys", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list: want 200, got %d", w.Code)
	}
	var keys []config.APIKey
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != "k1" {
		t.Fatalf("keys list: %+v", keys)
	}
	if keys[0].Secret != "redacted" {
		t.Fatalf("secret should be redacted on list, got %q", keys[0].Secret)
	}

	// GET one — also redacted.
	req = withAuth(httptest.NewRequest("GET", "/api/admin/apis/hello/keys/k1", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one: want 200, got %d", w.Code)
	}
	var single config.APIKey
	if err := json.Unmarshal(w.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.Secret != "redacted" {
		t.Fatalf("single GET secret should be redacted, got %q", single.Secret)
	}

	// DELETE.
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("DELETE", "/api/admin/apis/hello/keys/k1", nil))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: want 204, got %d", w.Code)
	}
}

func TestAdminAPIKeys_DupRejected(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mustCreate(t, srv, mux, `{"name":"hello","port":3000,"enabled":true}`)

	body := bytes.NewBufferString(`{"id":"k1","secret":"abc"}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis/hello/keys", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first POST: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	body = bytes.NewBufferString(`{"id":"k1","secret":"xyz"}`)
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis/hello/keys", body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("dup POST: want 409, got %d", w.Code)
	}
}

func TestAdminAPIKeys_UnknownAPI_404(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	body := bytes.NewBufferString(`{"id":"k1"}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis/nope/keys", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// mustCreate is a small wrapper for tests that need a starter API.
func mustCreate(t *testing.T, srv *Server, mux http.Handler, jsonBody string) {
	t.Helper()
	body := bytes.NewBufferString(jsonBody)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/apis", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("mustCreate(%s): want 201, got %d (%s)", jsonBody, w.Code, w.Body.String())
	}
}
