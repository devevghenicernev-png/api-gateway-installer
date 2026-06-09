package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyFileTo pins the contract of the new helper used by the v0.4.3
// swap fix: the destination is a brand-new path (no ETXTBSY risk), the
// contents are copied verbatim, and the mode is 0755 so rename(2) onto
// the live executable lands an immediately-runnable binary.
func TestCopyFileTo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	want := []byte("not a real binary, just bytes\n")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyFileTo(src, dst); err != nil {
		t.Fatalf("copyFileTo: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("contents mismatch: want=%q got=%q", want, got)
	}

	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o755 {
		t.Errorf("mode %v, want 0755", perm)
	}
}

// TestCopyFileTo_OverwritesStaleStaging verifies the O_TRUNC behaviour:
// if a previous crashed run left a stale staging file at the target
// path, the next swap attempt cleanly replaces its contents instead of
// appending. (Realistic on Linux when the upgrade process gets SIGKILL'd
// between staging and rename — rare, but the staging path is shared per
// PID and PIDs do recycle.)
func TestCopyFileTo_OverwritesStaleStaging(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	// Pre-existing stale staging contents.
	if err := os.WriteFile(dst, []byte("stale-junk-that-is-much-longer-than-new"), 0o644); err != nil {
		t.Fatalf("write stale dst: %v", err)
	}

	if err := copyFileTo(src, dst); err != nil {
		t.Fatalf("copyFileTo: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("expected truncate-then-write, got %q (length %d)", got, len(got))
	}
}
