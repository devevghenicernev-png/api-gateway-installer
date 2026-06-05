package migrate

import (
	"os"
	"path/filepath"
)

// writeFileMode is a tiny helper: ensure dir, write, chmod. Used for the
// secret-write fast-path that bypasses webhook.EnsureSecret's randomness.
func writeFileMode(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// renameOver is os.Rename with a "best-effort delete the target first if
// they're on different filesystems" fallback. Almost always a no-op on
// the rename path (atomic POSIX behaviour).
func renameOver(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Fallback for /tmp-on-tmpfs vs /etc-on-rootfs cross-device renames.
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}
