package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlainStringPassesThrough(t *testing.T) {
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "just-a-string")
	if err != nil || got != "just-a-string" {
		t.Fatalf("plain string: got %q err %v", got, err)
	}
}

func TestFileProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("supersecret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "file://"+path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "supersecret" {
		t.Errorf("expected supersecret; got %q", got)
	}
}

func TestFileProviderRefusesWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("oops"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := NewResolver()
	_, err := r.Resolve(context.Background(), "file://"+path)
	if err == nil {
		t.Errorf("expected error on world-readable secret")
	}
}

func TestEnvProvider(t *testing.T) {
	t.Setenv("APIGW_TEST_SECRET", "from-env")
	r := NewResolver()
	got, err := r.Resolve(context.Background(), "env://APIGW_TEST_SECRET")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "from-env" {
		t.Errorf("expected from-env; got %q", got)
	}
}

func TestUnknownSchemeErrors(t *testing.T) {
	r := NewResolver()
	_, err := r.Resolve(context.Background(), "unknown://x")
	if err == nil {
		t.Errorf("expected ErrUnknownScheme")
	}
}

func TestSchemeOf(t *testing.T) {
	cases := map[string]string{
		"vault://kv/data/x":  "vault",
		"file:///etc/foo":    "file",
		"aws-sm://billing":   "aws-sm",
		"plain":              "",
		"":                   "",
	}
	for in, want := range cases {
		if got := schemeOf(in); got != want {
			t.Errorf("schemeOf(%q) = %q; want %q", in, got, want)
		}
	}
}
