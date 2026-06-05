package tls

import (
	"fmt"
	"os"
	"path/filepath"
)

// webrootProvider implements lego's challenge.Provider for HTTP-01 by writing
// the challenge file under a directory that nginx already serves at
// /.well-known/acme-challenge/.
//
// Why we need this: the default http01.NewProviderServer binds :80 itself. If
// nginx is already on :80 (the normal case after `apigw install`), that
// bind fails. The webroot pattern is what certbot calls `--webroot` —
// nginx stays up, ACME pings nginx, nginx serves the challenge file from disk.
//
// Trade-off: needs a corresponding nginx `location /.well-known/acme-challenge/`
// block. Our tls-server.tmpl includes it; for HTTP-only deployments the
// nginx config also has to be patched. We accept that cost — the install
// flow writes the nginx config first, then obtains.
type webrootProvider struct {
	root string
}

// NewWebrootProvider returns a challenge.Provider that writes ACME tokens
// under <root>/.well-known/acme-challenge/. Creates the directory tree if
// it does not exist.
func NewWebrootProvider(root string) (*webrootProvider, error) {
	if root == "" {
		return nil, fmt.Errorf("webroot: root is empty")
	}
	if err := os.MkdirAll(filepath.Join(root, ".well-known", "acme-challenge"), 0o755); err != nil {
		return nil, fmt.Errorf("webroot: mkdir: %w", err)
	}
	return &webrootProvider{root: root}, nil
}

// Present writes the challenge response file. The path is fixed by the
// ACME spec: /.well-known/acme-challenge/<token>.
func (w *webrootProvider) Present(domain, token, keyAuth string) error {
	path := filepath.Join(w.root, ".well-known", "acme-challenge", token)
	// 0644 because nginx (a different user) reads it.
	return os.WriteFile(path, []byte(keyAuth), 0o644)
}

// CleanUp removes the file Present wrote. Best-effort — a leftover file is
// not a security risk, it's just dead bytes.
func (w *webrootProvider) CleanUp(domain, token, keyAuth string) error {
	path := filepath.Join(w.root, ".well-known", "acme-challenge", token)
	_ = os.Remove(path)
	return nil
}
