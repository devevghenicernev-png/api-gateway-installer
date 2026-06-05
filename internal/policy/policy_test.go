package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const requireHealthCheck = `package apigw.deploys
deny[msg] {
    input.action == "deploy.add"
    not input.after.health_check
    msg := "deploys must declare a health_check"
}
`

const requireAuth = `package apigw.apis
deny[msg] {
    input.action == "api.add"
    not input.after.jwt
    not input.after.mtls
    not input.after.basic_auth
    not input.after.forward_auth
    msg := "production APIs must have at least one auth mechanism"
}
`

func writePolicy(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestEvaluate_DenyMissingHealthCheck(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "01-deploys.rego", requireHealthCheck)

	e := NewEngine()
	n, err := e.LoadDir(dir)
	if err != nil || n != 1 {
		t.Fatalf("LoadDir: %v n=%d", err, n)
	}
	d, err := e.Evaluate(context.Background(), map[string]any{
		"action": "deploy.add",
		"after":  map[string]any{"name": "billing"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Denied {
		t.Fatalf("expected denied; got %+v", d)
	}
	if len(d.Reasons) != 1 || d.Reasons[0] != "deploys must declare a health_check" {
		t.Errorf("unexpected reasons: %v", d.Reasons)
	}
}

func TestEvaluate_AllowWithHealthCheck(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "01-deploys.rego", requireHealthCheck)

	e := NewEngine()
	_, _ = e.LoadDir(dir)
	d, _ := e.Evaluate(context.Background(), map[string]any{
		"action": "deploy.add",
		"after":  map[string]any{"name": "billing", "health_check": map[string]any{"path": "/h"}},
	})
	if d.Denied {
		t.Errorf("expected allowed; got %+v", d)
	}
}

func TestEvaluate_MultiplePolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicy(t, dir, "01-deploys.rego", requireHealthCheck)
	writePolicy(t, dir, "02-apis.rego", requireAuth)

	e := NewEngine()
	n, err := e.LoadDir(dir)
	if err != nil || n != 2 {
		t.Fatalf("LoadDir: %v n=%d", err, n)
	}
	d, _ := e.Evaluate(context.Background(), map[string]any{
		"action": "api.add",
		"after":  map[string]any{"name": "x"},
	})
	if !d.Denied {
		t.Errorf("expected denied for unauth API; got %+v", d)
	}
}

func TestEvaluate_EmptyEngineFailOpen(t *testing.T) {
	e := NewEngine()
	d, err := e.Evaluate(context.Background(), map[string]any{"action": "anything"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Denied {
		t.Errorf("empty engine should fail-open; got denied")
	}
}
