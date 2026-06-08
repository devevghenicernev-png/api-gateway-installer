package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

func ownerSecurity() config.Security {
	return config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "tk"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	}
}

func TestAdminStreamsCRUD(t *testing.T) {
	srv, mux, cfgPath := newTestServer(t, ownerSecurity())

	// POST create
	body := bytes.NewBufferString(`{
		"name":"mysql","protocol":"tcp","ListenPort":3306,
		"upstreams":[{"address":"127.0.0.1:3306"}],"enabled":true
	}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/streams", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST want 201, got %d (%s)", w.Code, w.Body.String())
	}

	// GET list
	req = withAuth(httptest.NewRequest("GET", "/api/admin/streams", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var list []config.Stream
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(list) != 1 || list[0].Name != "mysql" {
		t.Fatalf("list = %+v, want 1×mysql", list)
	}

	// GET one
	req = withAuth(httptest.NewRequest("GET", "/api/admin/streams/mysql", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one want 200, got %d (%s)", w.Code, w.Body.String())
	}

	// PUT update
	body = bytes.NewBufferString(`{
		"name":"mysql","protocol":"tcp","ListenPort":3307,
		"upstreams":[{"address":"127.0.0.1:3307"}],"enabled":false
	}`)
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/streams/mysql", body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d (%s)", w.Code, w.Body.String())
	}

	// Persisted?
	fresh, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c := fresh.(*config.Config)
	if len(c.Streams) != 1 || c.Streams[0].ListenPort != 3307 {
		t.Fatalf("stream not updated: %+v", c.Streams)
	}

	// DELETE
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("DELETE", "/api/admin/streams/mysql", nil))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE want 204, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminStreamsCreate_BadProtocolRejected(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	body := bytes.NewBufferString(`{
		"name":"ws","protocol":"foo","ListenPort":1234,
		"upstreams":[{"address":"127.0.0.1:1234"}]
	}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/streams", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for bad protocol, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminStreamsCreate_DupRejected(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	mk := func() *http.Request {
		body := bytes.NewBufferString(`{
			"name":"dup","protocol":"tcp","ListenPort":9999,
			"upstreams":[{"address":"127.0.0.1:9999"}]
		}`)
		return withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/streams", body))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, mk())
	if w.Code != http.StatusCreated {
		t.Fatalf("first create want 201, got %d (%s)", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, mk())
	if w.Code != http.StatusConflict {
		t.Fatalf("dup want 409, got %d", w.Code)
	}
}

func TestAdminConsumersCRUD(t *testing.T) {
	srv, mux, cfgPath := newTestServer(t, ownerSecurity())

	// POST
	body := bytes.NewBufferString(`{"id":"ci-bot","name":"CI Bot","groups":["robots","trusted"]}`)
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("POST", "/api/admin/consumers", body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST want 201, got %d (%s)", w.Code, w.Body.String())
	}

	// GET one
	req = withAuth(httptest.NewRequest("GET", "/api/admin/consumers/ci-bot", nil), "tk")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var got config.Consumer
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "ci-bot" || len(got.Groups) != 2 {
		t.Fatalf("consumer round-trip mismatch: %+v", got)
	}

	// PUT update groups
	body = bytes.NewBufferString(`{"id":"ci-bot","name":"CI Bot v2","groups":["robots"]}`)
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PUT", "/api/admin/consumers/ci-bot", body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d (%s)", w.Code, w.Body.String())
	}
	fresh, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	c := fresh.(*config.Config)
	if len(c.Security.Consumers) != 1 || len(c.Security.Consumers[0].Groups) != 1 {
		t.Fatalf("consumer not updated: %+v", c.Security.Consumers)
	}

	// DELETE
	req = withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("DELETE", "/api/admin/consumers/ci-bot", nil))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE want 204, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminConsumersUnknownGet404(t *testing.T) {
	_, mux, _ := newTestServer(t, ownerSecurity())
	req := withAuth(httptest.NewRequest("GET", "/api/admin/consumers/nope", nil), "tk")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestAdminStreamsMethodNotAllowed(t *testing.T) {
	srv, mux, _ := newTestServer(t, ownerSecurity())
	req := withCSRF(t, srv.Sec, "root", "tk", httptest.NewRequest("PATCH", "/api/admin/streams", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH on /streams want 405, got %d", w.Code)
	}
	if a := w.Header().Get("Allow"); a == "" {
		t.Fatalf("missing Allow header")
	}
}
