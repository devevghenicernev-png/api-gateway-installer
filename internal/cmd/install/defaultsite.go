package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// defaultSitePath is the canonical Debian/Ubuntu "welcome page" site nginx
// ships with by default. It declares `listen 80 default_server;` and
// `server_name _;`, which makes it win over apigw's `listen 80;` for any
// request whose Host header doesn't match a configured server_name —
// exactly the case for `curl http://<ip>/`.
//
// Variable, not const, so tests can point at a temp dir.
var defaultSitePath = func() string {
	return filepath.Join(paths.NginxSitesEnabled(), "default")
}

// disableNginxDefaultSite removes (or, when it's a real file, renames to
// .apigw-prev) the stock nginx default site. Returns:
//
//	(true,  nil) — actually disabled something this call
//	(false, nil) — nothing to do (already gone)
//	(false, err) — file exists but we couldn't move/unlink it
//
// We deliberately do NOT touch /etc/nginx/sites-available/default — only
// the symlink in sites-enabled. `apigw uninstall` re-symlinks it.
//
// macOS/Homebrew nginx has no sites-enabled layout — this is a no-op there.
func disableNginxDefaultSite() (bool, error) {
	path := defaultSitePath()
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("lstat %s: %w", path, err)
	}

	// Symlink → just remove. The original file in sites-available/ stays
	// untouched so `apigw uninstall` can re-create the link.
	if fi.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("unlink %s: %w", path, err)
		}
		return true, nil
	}

	// Regular file (rare — operator copied the conf to sites-enabled
	// directly). Move to .apigw-prev so we keep their content for
	// rollback. If a previous .apigw-prev exists, leave it alone and
	// just remove ours — overwriting older backups is a footgun.
	bak := path + ".apigw-prev"
	if _, statErr := os.Lstat(bak); statErr == nil {
		if rmErr := os.Remove(path); rmErr != nil {
			return false, fmt.Errorf("remove %s: %w", path, rmErr)
		}
		return true, nil
	}
	if err := os.Rename(path, bak); err != nil {
		return false, fmt.Errorf("backup %s -> %s: %w", path, bak, err)
	}
	return true, nil
}
