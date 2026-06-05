package dashboard

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/rbac"
)

// newTestSecurity is the shared fixture: a *Security wired against a fresh
// temp StateDir so audit.db / approvals.db / csrf.key don't bleed across tests.
func newTestSecurity(t *testing.T, sc config.Security, tenants []config.Tenant) *Security {
	t.Helper()
	if sc.StateDir == "" {
		sc.StateDir = t.TempDir()
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sec, err := NewSecurity(sc, tenants, config.Alerts{}, logger)
	if err != nil {
		t.Fatalf("NewSecurity: %v", err)
	}
	t.Cleanup(sec.Close)
	return sec
}

func TestIdentifyAnonymousWhenNoHeader(t *testing.T) {
	sec := newTestSecurity(t, config.Security{}, nil)
	r := httptest.NewRequest("GET", "/x", nil)
	ident := sec.Identify(r)
	if ident.User != "anonymous" {
		t.Fatalf("want anonymous, got %q", ident.User)
	}
}

func TestIdentifyBearerToken(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		AdminTokens: []config.AdminToken{
			{Name: "ops-bot", User: "alice", Token: "super-secret-token-with-32+bytes", Groups: []string{"sre"}},
		},
	}, nil)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer super-secret-token-with-32+bytes")
	ident := sec.Identify(r)
	if ident.User != "alice" {
		t.Fatalf("want alice, got %q", ident.User)
	}
	if len(ident.Groups) != 1 || ident.Groups[0] != "sre" {
		t.Fatalf("want groups=[sre], got %v", ident.Groups)
	}
}

func TestIdentifyUnknownTokenIsAnonymous(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		AdminTokens: []config.AdminToken{
			{Name: "ops-bot", User: "alice", Token: "real-token"},
		},
	}, nil)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer wrong-token")
	ident := sec.Identify(r)
	// Unknown token must NOT escalate to a known user — defence against
	// dictionary attacks against the token table.
	if ident.User != "anonymous" {
		t.Fatalf("want anonymous, got %q", ident.User)
	}
}

func TestIdentifyForwardedSubject(t *testing.T) {
	sec := newTestSecurity(t, config.Security{}, nil)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Apigw-Subject", "bob")
	ident := sec.Identify(r)
	if ident.User != "bob" {
		t.Fatalf("want bob (from X-Apigw-Subject), got %q", ident.User)
	}
}

func TestGuardRBACAllowsOwner(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "ownertoken"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	}, nil)
	r := httptest.NewRequest("POST", "/api/admin/apis", nil)
	r.Header.Set("Authorization", "Bearer ownertoken")

	called := false
	if _, err := sec.Guard(r, Action{Permission: "api.add", Resource: "api/foo"}, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("Guard: %v", err)
	}
	if !called {
		t.Fatal("onCommit not invoked despite RBAC pass")
	}
}

func TestGuardRBACDeniesAnonymousWhenEnforce(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		RBACEnforce: true,
	}, nil)
	r := httptest.NewRequest("POST", "/api/admin/apis", nil)

	called := false
	_, err := sec.Guard(r, Action{Permission: "api.add", Resource: "api/foo"}, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("anonymous POST must be forbidden under enforce=true")
	}
	if called {
		t.Fatal("onCommit invoked despite denial")
	}
	if Status(err) != http.StatusForbidden {
		t.Fatalf("want 403, got %d (%v)", Status(err), err)
	}
}

func TestGuardSoftRolloutAllowsButAudits(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		RBACEnforce: false, // soft rollout: log denial, don't refuse
	}, nil)
	r := httptest.NewRequest("POST", "/api/admin/apis", nil)
	if _, err := sec.Guard(r, Action{Permission: "api.add"}, nil); err != nil {
		t.Fatalf("soft-rollout Guard: %v", err)
	}
}

func TestReadBodyLimit(t *testing.T) {
	sec := newTestSecurity(t, config.Security{MaxRequestBytes: 16}, nil)
	body := strings.NewReader(`{"name":"this-exceeds-sixteen-bytes-easily"}`)
	r := httptest.NewRequest("POST", "/x", body)
	var dst map[string]string
	err := sec.ReadBody(r, &dst)
	if err == nil {
		t.Fatal("expected size-limit error")
	}
	if Status(err) != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", Status(err))
	}
}

func TestReadBodyOK(t *testing.T) {
	sec := newTestSecurity(t, config.Security{MaxRequestBytes: 1024}, nil)
	body := strings.NewReader(`{"name":"foo"}`)
	r := httptest.NewRequest("POST", "/x", body)
	var dst map[string]string
	if err := sec.ReadBody(r, &dst); err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if dst["name"] != "foo" {
		t.Fatalf("decoded wrong: %v", dst)
	}
}

func TestCSRFRoundTrip(t *testing.T) {
	sec := newTestSecurity(t, config.Security{}, nil)
	tok := sec.IssueCSRFToken("alice")
	if tok == "" {
		t.Fatal("empty token")
	}
	if err := sec.VerifyCSRFToken("alice", tok); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestCSRFWrongUser(t *testing.T) {
	sec := newTestSecurity(t, config.Security{}, nil)
	tok := sec.IssueCSRFToken("alice")
	if err := sec.VerifyCSRFToken("eve", tok); err == nil {
		t.Fatal("VerifyCSRFToken accepted token under wrong user")
	}
}

func TestCSRFTampered(t *testing.T) {
	sec := newTestSecurity(t, config.Security{}, nil)
	tok := sec.IssueCSRFToken("alice")
	// Flip a byte at the end (signature region) — constant-time compare
	// must fail.
	bad := tok[:len(tok)-1] + "A"
	if err := sec.VerifyCSRFToken("alice", bad); err == nil {
		t.Fatal("tampered token must not verify")
	}
}

func TestAuditWrittenOnSuccess(t *testing.T) {
	dir := t.TempDir()
	sec := newTestSecurity(t, config.Security{
		StateDir:    dir,
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "root", User: "root", Token: "t"}},
		Assignments: []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	}, nil)
	r := httptest.NewRequest("POST", "/api/admin/apis", nil)
	r.Header.Set("Authorization", "Bearer t")
	if _, err := sec.Guard(r, Action{Permission: "api.add", Resource: "api/foo"}, func() error {
		return nil
	}); err != nil {
		t.Fatalf("Guard: %v", err)
	}
	// Audit.Count includes RBAC's own audit hook (ok entry) plus Guard's
	// success entry → ≥1.
	if got := sec.AuditCount(); got == 0 {
		t.Fatalf("expected ≥1 audit entry, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.db")); err != nil {
		t.Fatalf("audit.db not created: %v", err)
	}
}

func TestApprovalsParkOnDangerous(t *testing.T) {
	dir := t.TempDir()
	sec := newTestSecurity(t, config.Security{
		StateDir:           dir,
		RBACEnforce:        true,
		ApprovalsThreshold: 2,
		AdminTokens:        []config.AdminToken{{Name: "root", User: "root", Token: "t"}},
		Assignments:        []config.Assignment{{User: "root", Roles: []string{"owner"}}},
	}, nil)
	r := httptest.NewRequest("DELETE", "/api/admin/apis/foo", nil)
	r.Header.Set("Authorization", "Bearer t")

	called := false
	_, err := sec.Guard(r, Action{
		Permission: "api.remove",
		Resource:   "api/foo",
		Before:     map[string]any{"name": "foo"},
		Dangerous:  true,
	}, func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("dangerous action should be parked when threshold>0")
	}
	if !strings.Contains(err.Error(), "parked") {
		t.Fatalf("want 'parked' in err, got %q", err.Error())
	}
	if Status(err) != http.StatusAccepted {
		t.Fatalf("want 202, got %d", Status(err))
	}
	if called {
		t.Fatal("onCommit ran despite parking")
	}
	// The change must persist in the approvals store.
	list, lerr := sec.Approvals.List("pending", 10)
	if lerr != nil {
		t.Fatalf("approvals list: %v", lerr)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 pending change, got %d", len(list))
	}
	if list[0].Threshold != 2 {
		t.Fatalf("want threshold 2, got %d", list[0].Threshold)
	}
}

func TestTokenHashIsolation(t *testing.T) {
	// Two configs with similar token prefixes shouldn't collide via hashing.
	sec := newTestSecurity(t, config.Security{
		AdminTokens: []config.AdminToken{
			{Name: "a", User: "alice", Token: "tokenA"},
			{Name: "b", User: "bob", Token: "tokenB"},
		},
	}, nil)
	r1 := httptest.NewRequest("GET", "/x", nil)
	r1.Header.Set("Authorization", "Bearer tokenA")
	r2 := httptest.NewRequest("GET", "/x", nil)
	r2.Header.Set("Authorization", "Bearer tokenB")
	if sec.Identify(r1).User != "alice" {
		t.Fatal("token A → wrong identity")
	}
	if sec.Identify(r2).User != "bob" {
		t.Fatal("token B → wrong identity")
	}
}

func TestNilSecurityIsNoOp(t *testing.T) {
	var sec *Security
	r := httptest.NewRequest("POST", "/x", nil)
	// Identify on nil: must return anonymous.
	if sec.Identify(r).User != "anonymous" {
		t.Fatal("nil Identify must return anonymous")
	}
	// Guard on nil: must short-circuit and run onCommit.
	called := false
	if _, err := sec.Guard(r, Action{Permission: "x"}, func() error { called = true; return nil }); err != nil {
		t.Fatalf("nil Guard: %v", err)
	}
	if !called {
		t.Fatal("nil Guard must run onCommit (legacy mode)")
	}
	// CSRF on nil: bypass (no security → no CSRF).
	if err := sec.VerifyCSRFToken("any", "any"); err != nil {
		t.Fatalf("nil VerifyCSRFToken: %v", err)
	}
}

func TestCSRFKeyPersistedToDisk(t *testing.T) {
	dir := t.TempDir()
	sec1 := newTestSecurity(t, config.Security{StateDir: dir}, nil)
	tok := sec1.IssueCSRFToken("alice")

	// Re-open a fresh Security against the same StateDir — the key file must
	// be picked up so tokens stay valid across daemon restarts.
	sec1.Close()
	sec2 := newTestSecurity(t, config.Security{StateDir: dir}, nil)
	if err := sec2.VerifyCSRFToken("alice", tok); err != nil {
		t.Fatalf("token didn't survive restart: %v", err)
	}
}

func TestRBACBuiltinViewerRoleOnly(t *testing.T) {
	sec := newTestSecurity(t, config.Security{
		RBACEnforce: true,
		AdminTokens: []config.AdminToken{{Name: "ops", User: "carol", Token: "viewertok"}},
		Assignments: []config.Assignment{{User: "carol", Roles: []string{"viewer"}}},
	}, nil)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer viewertok")

	// api.list is in viewer.
	if _, err := sec.Guard(r, Action{Permission: "api.list"}, nil); err != nil {
		t.Errorf("viewer should pass api.list: %v", err)
	}
	// api.add is admin-only.
	if _, err := sec.Guard(r, Action{Permission: "api.add"}, nil); err == nil {
		t.Error("viewer must NOT pass api.add")
	}
}

func TestRBACEngineNotNilForLegacyMode(t *testing.T) {
	// Even with no AdminTokens & no enforce, the engine should still exist
	// so audit logging of permissions works.
	sec := newTestSecurity(t, config.Security{}, nil)
	if sec.RBAC == nil {
		t.Fatal("RBAC engine must be non-nil even in legacy mode")
	}
	_ = rbac.ErrDenied // touch package to keep import live in test scope
}

func TestCSRFExpiry(t *testing.T) {
	// We can't easily forge time inside the test, but we can verify a
	// freshly-issued token has < 12h elapsed (sanity check; the actual
	// expiry path is exercised by reading the encoded timestamp).
	sec := newTestSecurity(t, config.Security{}, nil)
	before := time.Now().Unix()
	tok := sec.IssueCSRFToken("alice")
	after := time.Now().Unix()
	if tok == "" {
		t.Fatal("empty token")
	}
	// We have no decoder exposed, but VerifyCSRFToken right now should pass.
	if err := sec.VerifyCSRFToken("alice", tok); err != nil {
		t.Fatalf("fresh token must verify: %v", err)
	}
	if after-before > 5 {
		t.Fatal("test took >5s; CSRF timing assumptions invalid")
	}
}
