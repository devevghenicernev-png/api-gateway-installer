package install

import (
	"os"
	"path/filepath"
	"testing"
)

// patchDefaultSitePath redirects defaultSitePath() at a temp file for the
// duration of one test. Returns the temp path and a restore func.
func patchDefaultSitePath(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	enabled := filepath.Join(dir, "sites-enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	available := filepath.Join(dir, "sites-available")
	if err := os.MkdirAll(available, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(enabled, "default")
	prev := defaultSitePath
	defaultSitePath = func() string { return target }
	return target, func() { defaultSitePath = prev }
}

func TestDisableNginxDefaultSite_NotPresent(t *testing.T) {
	_, restore := patchDefaultSitePath(t)
	defer restore()

	disabled, err := disableNginxDefaultSite()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if disabled {
		t.Fatal("nothing to disable, but disabled=true")
	}
}

func TestDisableNginxDefaultSite_Symlink(t *testing.T) {
	target, restore := patchDefaultSitePath(t)
	defer restore()

	// Real source file the symlink points to.
	src := filepath.Join(filepath.Dir(filepath.Dir(target)), "sites-available", "default")
	if err := os.WriteFile(src, []byte("server { listen 80 default_server; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, target); err != nil {
		t.Fatal(err)
	}

	disabled, err := disableNginxDefaultSite()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !disabled {
		t.Fatal("expected disabled=true")
	}
	// Symlink gone…
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink still present: %v", err)
	}
	// …but source file preserved.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source file should be preserved, got %v", err)
	}
}

func TestDisableNginxDefaultSite_RealFile(t *testing.T) {
	target, restore := patchDefaultSitePath(t)
	defer restore()

	if err := os.WriteFile(target, []byte("server { listen 80 default_server; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	disabled, err := disableNginxDefaultSite()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !disabled {
		t.Fatal("expected disabled=true")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still at original path: %v", err)
	}
	if _, err := os.Stat(target + ".apigw-prev"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestDisableNginxDefaultSite_PreservesExistingBackup(t *testing.T) {
	target, restore := patchDefaultSitePath(t)
	defer restore()

	if err := os.WriteFile(target+".apigw-prev", []byte("old-backup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("current-default\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	disabled, err := disableNginxDefaultSite()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !disabled {
		t.Fatal("expected disabled=true")
	}
	// Existing backup must NOT be overwritten — older snapshot wins.
	body, err := os.ReadFile(target + ".apigw-prev")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "old-backup\n" {
		t.Fatalf("backup overwritten: %q", string(body))
	}
}
