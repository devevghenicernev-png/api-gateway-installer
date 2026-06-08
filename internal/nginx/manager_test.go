package nginx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// captureValidate is a stub validator that records the path argument it
// received so tests can assert WriteAndReload calls validate("") (plain
// `nginx -t`) and not validate(m.sitePath) (which would re-introduce
// BUG-1: "server directive is not allowed here").
type captureValidate struct {
	calls []string
	fail  error
}

func (c *captureValidate) fn(path string) error {
	c.calls = append(c.calls, path)
	return c.fail
}

func withTempHTTPConf(t *testing.T) (cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	prev := HTTPConfPath
	HTTPConfPath = filepath.Join(dir, "apigw-http.conf")
	return func() { HTTPConfPath = prev }
}

// TestWriteAndReload_ValidateCalledWithEmpty — direct regression for BUG-1.
//
// The validator stub records its `path` argument; we assert it is "" so we
// know WriteAndReload runs plain `nginx -t` (which traverses the real
// nginx.conf include chain in production) rather than `nginx -t -c <site>`
// (which always fails on the server-block fragment).
func TestWriteAndReload_ValidateCalledWithEmpty(t *testing.T) {
	defer withTempHTTPConf(t)()
	dir := t.TempDir()
	sitePath := filepath.Join(dir, "sites-available", "apigw.conf")
	enabled := filepath.Join(dir, "sites-enabled", "apigw.conf")

	cap := &captureValidate{}
	m := NewManagerWithFS(afero.NewOsFs(), sitePath, enabled,
		func() error { return nil }, cap.fn)

	cfg := config.Defaults()
	if err := m.WriteAndReload(&cfg); err != nil {
		t.Fatalf("WriteAndReload: %v", err)
	}
	if len(cap.calls) != 1 {
		t.Fatalf("validate called %d times, want 1: %v", len(cap.calls), cap.calls)
	}
	if cap.calls[0] != "" {
		t.Fatalf("validate path = %q, want empty (plain nginx -t)", cap.calls[0])
	}
}

// TestWriteAndReload_ValidateFailRemovesSymlink — regression for BUG-2.
//
// After a failed validate on a fresh host (no previous snapshot), the
// sites-enabled symlink we created BEFORE validate must be removed so the
// real /etc/nginx/sites-enabled doesn't end up pointing at a deleted file
// (which would brick `nginx -t` / start with "open() failed").
func TestWriteAndReload_ValidateFailRemovesSymlink(t *testing.T) {
	defer withTempHTTPConf(t)()
	dir := t.TempDir()
	sitePath := filepath.Join(dir, "sites-available", "apigw.conf")
	enabled := filepath.Join(dir, "sites-enabled", "apigw.conf")

	cap := &captureValidate{fail: errors.New("nginx -t: emerg foo")}
	m := NewManagerWithFS(afero.NewOsFs(), sitePath, enabled,
		func() error { return nil }, cap.fn)

	cfg := config.Defaults()
	err := m.WriteAndReload(&cfg)
	if err == nil {
		t.Fatal("WriteAndReload should have failed (validate stubbed to error)")
	}

	if _, err := os.Lstat(enabled); err == nil {
		t.Fatalf("dangling symlink left behind at %s after failed validate", enabled)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected Lstat error: %v", err)
	}
}

// TestWriteAndReload_SymlinkCreatedOnSuccess — happy path: validate passes,
// the sites-enabled symlink is in place and points at the freshly written
// site file.
func TestWriteAndReload_SymlinkCreatedOnSuccess(t *testing.T) {
	defer withTempHTTPConf(t)()
	dir := t.TempDir()
	sitePath := filepath.Join(dir, "sites-available", "apigw.conf")
	enabled := filepath.Join(dir, "sites-enabled", "apigw.conf")

	cap := &captureValidate{}
	m := NewManagerWithFS(afero.NewOsFs(), sitePath, enabled,
		func() error { return nil }, cap.fn)

	cfg := config.Defaults()
	if err := m.WriteAndReload(&cfg); err != nil {
		t.Fatalf("WriteAndReload: %v", err)
	}
	target, err := os.Readlink(enabled)
	if err != nil {
		t.Fatalf("Readlink(enabled): %v", err)
	}
	if target != sitePath {
		t.Fatalf("symlink target = %q, want %q", target, sitePath)
	}
}
