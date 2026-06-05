package tls

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSnapshotExistingForcesPrivkeyMode verifies the core security fix: even
// when the live privkey file is world-readable (e.g. left over from a manual
// copy with `cp` and a permissive umask), the .bak snapshot must land at
// 0o600 so a rollback can't reintroduce the leak.
func TestSnapshotExistingForcesPrivkeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	dir := t.TempDir()
	live := filepath.Join(dir, "privkey.pem")
	bak := filepath.Join(dir, "privkey.pem.bak")

	// Plant a world-readable privkey.
	if err := os.WriteFile(live, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(live, 0o644); err != nil {
		t.Fatalf("chmod live: %v", err)
	}

	if !snapshotExisting(live, bak, 0o600) {
		t.Fatal("snapshotExisting returned false")
	}

	info, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("stat bak: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bak perms = %o, want 0o600", info.Mode().Perm())
	}
}

// TestSnapshotExistingInheritsModeWhenNoForce verifies the non-forced path
// (fullchain) keeps its original mode — fullchain CAN be world-readable.
func TestSnapshotExistingInheritsModeWhenNoForce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	dir := t.TempDir()
	live := filepath.Join(dir, "fullchain.pem")
	bak := filepath.Join(dir, "fullchain.pem.bak")

	if err := os.WriteFile(live, []byte("-----BEGIN CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(live, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if !snapshotExisting(live, bak, 0) { // 0 = inherit
		t.Fatal("snapshotExisting returned false")
	}

	info, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("fullchain bak perms = %o, want 0o644", info.Mode().Perm())
	}
}

func TestSnapshotExistingMissingFile(t *testing.T) {
	dir := t.TempDir()
	if snapshotExisting(filepath.Join(dir, "nope"), filepath.Join(dir, "bak"), 0o600) {
		t.Fatal("missing live file must return false")
	}
}
