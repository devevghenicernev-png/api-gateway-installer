package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// fileProvider handles `file://<path>` references. The path is read
// verbatim — no decoding, trimming, or templating. Use cases:
//
//	file:///etc/apigw/secrets/jwt.key
//	file:///run/secrets/db_password    (Docker secrets)
//
// File mode is checked at resolution: we refuse to read world-readable
// secrets (perm & 0o004 != 0) to nudge operators toward 0600/0640.
type fileProvider struct{}

func (fileProvider) Scheme() string { return "file" }

func (fileProvider) Resolve(_ context.Context, ref string) (string, error) {
	path := strings.TrimPrefix(ref, "file://")
	if path == "" {
		return "", fmt.Errorf("file: empty path in %q", ref)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o004 != 0 {
		return "", fmt.Errorf("file: %s is world-readable (mode %o) — chmod 0600", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("file: read %s: %w", path, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}
