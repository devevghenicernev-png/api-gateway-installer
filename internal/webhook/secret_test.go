package webhook

import (
	"os"
	"testing"
	"time"
)

// TestRotateSecret_GracePeriod verifies that after RotateSecret, the
// previous secret continues to verify deliveries for the grace window
// (in-flight GitHub deliveries don't fail HMAC immediately on rotation).
func TestRotateSecret_GracePeriod(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APIGW_CONFIG_DIR", dir)

	const name = "test-deploy"

	first, err := EnsureSecret(name)
	if err != nil {
		t.Fatalf("EnsureSecret: %v", err)
	}

	second, err := RotateSecret(name)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if second == first {
		t.Fatalf("rotation produced identical secret")
	}

	secrets, err := LoadValidSecrets(name)
	if err != nil {
		t.Fatalf("LoadValidSecrets: %v", err)
	}
	if len(secrets) != 2 {
		t.Fatalf("want 2 valid secrets (live + grace), got %d", len(secrets))
	}
	want := map[string]bool{first: false, second: false}
	for _, s := range secrets {
		if _, ok := want[string(s)]; ok {
			want[string(s)] = true
		}
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("secret %q missing from LoadValidSecrets", s)
		}
	}
}

// TestRotateSecret_GraceExpires verifies that once RotationGrace has
// elapsed, the previous secret is no longer accepted and its .prev file
// is cleaned up.
func TestRotateSecret_GraceExpires(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APIGW_CONFIG_DIR", dir)
	const name = "test-deploy"

	first, err := EnsureSecret(name)
	if err != nil {
		t.Fatalf("EnsureSecret: %v", err)
	}
	if _, err := RotateSecret(name); err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	// Backdate the .prev mtime past the grace window.
	old := time.Now().Add(-2 * RotationGrace)
	if err := os.Chtimes(PrevSecretPath(name), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	secrets, err := LoadValidSecrets(name)
	if err != nil {
		t.Fatalf("LoadValidSecrets: %v", err)
	}
	if len(secrets) != 1 {
		t.Fatalf("want 1 valid secret after grace expiry, got %d", len(secrets))
	}
	if string(secrets[0]) == first {
		t.Fatalf("expired previous secret still accepted")
	}
	if _, err := os.Stat(PrevSecretPath(name)); !os.IsNotExist(err) {
		t.Errorf("expired .prev file should be cleaned up; got %v", err)
	}
}

// TestVerifyAny_AcceptsEitherSecret confirms HMAC verification accepts a
// signature produced by EITHER the live or the previous-secret-within-grace.
func TestVerifyAny_AcceptsEitherSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APIGW_CONFIG_DIR", dir)
	const name = "demo"

	old, err := EnsureSecret(name)
	if err != nil {
		t.Fatalf("EnsureSecret: %v", err)
	}
	newS, err := RotateSecret(name)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}

	body := []byte(`{"event":"push"}`)
	sigOld := Sign([]byte(old), body)
	sigNew := Sign([]byte(newS), body)

	secrets, err := LoadValidSecrets(name)
	if err != nil {
		t.Fatalf("LoadValidSecrets: %v", err)
	}
	if !verifyAny(secrets, body, sigNew) {
		t.Error("new secret signature should verify")
	}
	if !verifyAny(secrets, body, sigOld) {
		t.Error("old secret signature should verify within grace window")
	}
	if verifyAny(secrets, body, "sha256=deadbeef") {
		t.Error("bogus signature must NOT verify")
	}
}
