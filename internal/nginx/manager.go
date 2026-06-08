package nginx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/afero"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// SitePath is where apigw writes its generated nginx server block.
//
// On Debian/Ubuntu (Linux): /etc/nginx/sites-available/apigw.conf with a
// symlink in sites-enabled (apigw owns one filename — never the directory —
// so user-managed sites coexist).
//
// On macOS (Homebrew nginx): /opt/homebrew/etc/nginx/servers/apigw.conf (or
// /usr/local on Intel). Homebrew nginx has no sites-enabled split; it loads
// every file under servers/, so EnabledLink == SitePath there.
//
// These are var, not const, because the values are os-dependent.
var (
	SitePath     = paths.NginxSitesAvailable() + "/apigw.conf"
	EnabledLink  = paths.NginxSitesEnabled() + "/apigw.conf"
	HTTPConfPath = paths.NginxConfD() + "/apigw-http.conf"
	// Stream block lives OUTSIDE conf.d/ because nginx auto-includes
	// conf.d/*.conf inside http{}, and stream{} content can't sit there.
	// We park it next to nginx.conf and the marker-block in nginx.conf
	// loads it from main scope.
	StreamConfPath  = paths.NginxConfDir() + "/apigw-stream.conf"
	BackupExtension = ".apigw-prev"
)

// Manager is the operational counterpart to Generator: it writes config to
// disk under a temp file, validates `nginx -t`, then renames into place and
// reloads. If validation fails, it leaves the previous file untouched.
type Manager struct {
	fs        afero.Fs
	gen       *Generator
	sitePath  string
	enabled   string
	reloadCmd func() error // injectable for tests; default systemctl reload nginx
	validate  func(path string) error

	// Metrics, when non-nil, receives a result-labeled increment on every
	// WriteAndReload call ("ok" | "validate_fail" | "rollback").
	Metrics ReloadCounter
}

// ReloadCounter is the minimal surface Manager depends on for Prometheus.
type ReloadCounter interface {
	IncNginxReload(result string)
}

// NewManager wires a Manager against the real OS filesystem. Tests use
// NewManagerWithFS for an afero.MemMapFs and mocked reload/validate hooks.
func NewManager() *Manager {
	return &Manager{
		fs:        afero.NewOsFs(),
		gen:       NewGenerator(),
		sitePath:  SitePath,
		enabled:   EnabledLink,
		reloadCmd: defaultReload,
		validate:  defaultValidate,
	}
}

// NewManagerWithFS lets tests inject a memory filesystem and stubbed reload.
func NewManagerWithFS(fs afero.Fs, sitePath, enabled string, reload func() error, validate func(string) error) *Manager {
	return &Manager{
		fs:        fs,
		gen:       NewGenerator(),
		sitePath:  sitePath,
		enabled:   enabled,
		reloadCmd: reload,
		validate:  validate,
	}
}

// Render produces both files apigw would write; no side effects.
// Useful for `apigw install --dry-run` and golden-file tests.
func (m *Manager) Render(cfg *config.Config) (serverBytes, httpBytes []byte, err error) {
	return m.gen.Render(cfg)
}

// Validate runs `nginx -t` against the current on-disk config. Does NOT
// re-render. Use WriteAndReload to apply a new config.
func (m *Manager) Validate() error {
	return m.validate("")
}

// Reload sends SIGHUP via systemctl. Idempotent; safe to call repeatedly.
func (m *Manager) Reload() error { return m.reloadCmd() }

// WriteAndReload is the atomic apply: render → tmp → validate → rename →
// reload. On any failure the previous file is restored.
//
// Sequence:
//  1. Render the new config to bytes.
//  2. If the destination exists, copy it to <path>.apigw-prev.
//  3. Write the new bytes to <path>.new, fsync, rename onto <path>.
//  4. nginx -t — if it fails, restore <path>.apigw-prev → <path> and return.
//  5. systemctl reload nginx.
func (m *Manager) WriteAndReload(cfg *config.Config) error {
	serverBody, httpBody, err := m.gen.Render(cfg)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	streamBody, err := m.gen.RenderStream(cfg)
	if err != nil {
		return fmt.Errorf("render stream: %w", err)
	}

	// Snapshot + stage server file + http file simultaneously. If either
	// rename fails, restore both snapshots so the live nginx state stays
	// consistent.
	if err := m.snapshotAndStage(m.sitePath, serverBody); err != nil {
		return err
	}
	if err := m.snapshotAndStage(HTTPConfPath, httpBody); err != nil {
		// server file already staged; roll back the .new but leave the
		// previously-committed server file alone (no rename happened yet
		// for either — both .new files just need cleanup).
		_ = m.fs.Remove(m.sitePath + ".new")
		return err
	}
	// Commit phase: rename .new → live for both files. If the second
	// rename fails, restore the first from its snapshot.
	if err := m.fs.Rename(m.sitePath+".new", m.sitePath); err != nil {
		_ = m.fs.Remove(HTTPConfPath + ".new")
		return fmt.Errorf("rename server: %w", err)
	}
	if err := m.fs.Rename(HTTPConfPath+".new", HTTPConfPath); err != nil {
		// Restore server from snapshot so http remains untouched.
		if prev, rerr := afero.ReadFile(m.fs, m.sitePath+BackupExtension); rerr == nil {
			_ = afero.WriteFile(m.fs, m.sitePath, prev, 0o640)
		}
		return fmt.Errorf("rename http: %w", err)
	}

	// Stream (TCP/UDP) — write the conf file when present, remove when not.
	// nginx loads it via a `stream { include /etc/nginx/conf.d/apigw-stream.conf; }`
	// block injected into nginx.conf by ensureStreamInclude (idempotent;
	// removed by Uninstall + when the streams list goes back to empty).
	if len(streamBody) > 0 {
		if err := afero.WriteFile(m.fs, StreamConfPath, streamBody, 0o640); err != nil {
			return fmt.Errorf("write stream: %w", err)
		}
		if err := ensureStreamInclude(true); err != nil {
			return fmt.Errorf("nginx.conf stream-include: %w", err)
		}
	} else {
		// No streams: remove the conf so an old apigw-stream.conf doesn't
		// linger; strip the include from nginx.conf so empty stream{}
		// doesn't sit in main scope.
		_ = m.fs.Remove(StreamConfPath)
		if err := ensureStreamInclude(false); err != nil {
			return fmt.Errorf("nginx.conf stream-include: %w", err)
		}
	}

	// Track whether ensureEnabled() actually created the symlink in this call
	// so rollbackBoth() can remove it if validate fails. Without this, the
	// FIRST install on a host (no .apigw-prev snapshot to restore) ends with
	// a dangling sites-enabled/apigw.conf → sites-available/apigw.conf
	// symlink while the target was deleted by rollback. Every subsequent
	// `nginx -t` / reload / start then dies with "open() … failed (2)".
	createdSymlink := m.ensureEnabled()

	// validate("") runs plain `nginx -t` — it walks the real
	// /etc/nginx/nginx.conf, picking up our file via the existing
	// include sites-enabled/*; conf.d/* directives. Passing m.sitePath as
	// -c instead would make nginx parse a server-block fragment as a MAIN-
	// context file, which always fails with `"server" directive is not
	// allowed here`. Our fragment is server-context by design.
	if err := m.validate(""); err != nil {
		// Roll back BOTH files; also remove the symlink we just created so
		// stock nginx doesn't trip over a dangling include.
		m.rollbackBoth()
		if createdSymlink {
			_ = os.Remove(m.enabled)
		}
		if m.Metrics != nil {
			m.Metrics.IncNginxReload("validate_fail")
		}
		return fmt.Errorf("nginx validate failed (rolled back): %w", err)
	}

	if err := m.reloadCmd(); err != nil {
		if m.Metrics != nil {
			m.Metrics.IncNginxReload("reload_fail")
		}
		// Reload failure: leave new config in place; nginx still runs old config.
		return fmt.Errorf("nginx reload: %w", err)
	}
	if m.Metrics != nil {
		m.Metrics.IncNginxReload("ok")
	}
	return nil
}

// snapshotAndStage handles the per-file "snapshot existing → write tmp →
// fsync" sub-step shared by sitePath and HTTPConfPath. The actual rename
// onto the live path is deferred to the caller so it can ensure both files
// commit together.
func (m *Manager) snapshotAndStage(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := m.fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if existing, err := afero.ReadFile(m.fs, path); err == nil {
		if werr := afero.WriteFile(m.fs, path+BackupExtension, existing, 0o640); werr != nil {
			return fmt.Errorf("snapshot %s: %w", path, werr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read previous %s: %w", path, err)
	}
	tmp := path + ".new"
	if err := afero.WriteFile(m.fs, tmp, body, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if _, isOsFs := m.fs.(*afero.OsFs); isOsFs {
		if f, ferr := os.OpenFile(tmp, os.O_RDWR, 0); ferr == nil {
			_ = f.Sync()
			_ = f.Close()
		}
	}
	return nil
}

// rollbackBoth restores both the server and http config files from their
// .apigw-prev snapshots. Best-effort — partial restore is still better
// than leaving torn config.
func (m *Manager) rollbackBoth() {
	for _, p := range []string{m.sitePath, HTTPConfPath} {
		if prev, rerr := afero.ReadFile(m.fs, p+BackupExtension); rerr == nil {
			_ = afero.WriteFile(m.fs, p, prev, 0o640)
		} else {
			_ = m.fs.Remove(p)
		}
	}
}

// ensureEnabled creates the sites-enabled symlink if missing. Best-effort.
// Only runs against the OS filesystem (afero.OsFs); skips for memory fs.
//
// Returns true ONLY when this call actually created the symlink (so the
// caller can roll it back if a subsequent validate fails). Returns false
// when the symlink already existed or when running on a non-OS fs.
func (m *Manager) ensureEnabled() bool {
	osFs, ok := m.fs.(*afero.OsFs)
	_ = osFs
	if !ok {
		return false
	}
	if _, err := os.Lstat(m.enabled); err == nil {
		return false
	}
	_ = os.MkdirAll(filepath.Dir(m.enabled), 0o755)
	if err := os.Symlink(m.sitePath, m.enabled); err != nil {
		return false
	}
	return true
}

// defaultReload triggers an nginx reload. On the most common fresh-host
// state (nginx installed by the package manager but never started),
// `nginx -s reload` fails with `invalid PID number "" in /run/nginx.pid`
// because there's no master process to signal. In that case we promote
// to `systemctl start nginx` rather than reload, so `apigw install` on
// a clean box doesn't fail at the very last step (L-3 in
// DOCKER_TEST_REPORT.md).
//
// Order:
//  1. `nginx -s reload` (the common, fast path on a running gateway)
//  2. if nginx is inactive → `systemctl start nginx` (or `enable --now`)
//  3. else `systemctl reload nginx` (privilege/PID-file fallback)
func defaultReload() error {
	cmd := exec.Command("nginx", "-s", "reload")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		return nil
	} else {
		nginxStderr := stderr.String()
		// Distinguish "master isn't running" from other reload failures.
		// `systemctl is-active --quiet nginx` returns 0 only when active.
		inactive := exec.Command("systemctl", "is-active", "--quiet", "nginx").Run() != nil
		if inactive {
			stderr.Reset()
			start := exec.Command("systemctl", "start", "nginx")
			start.Stderr = &stderr
			if startErr := start.Run(); startErr != nil {
				return fmt.Errorf("nginx start: %w (nginx -s: %s) (systemctl start: %s)", startErr, nginxStderr, stderr.String())
			}
			return nil
		}
		stderr.Reset()
		cmd2 := exec.Command("systemctl", "reload", "nginx")
		cmd2.Stderr = &stderr
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("nginx reload: %w (nginx -s: %s) (systemctl: %s)", err2, nginxStderr, stderr.String())
		}
	}
	return nil
}

// defaultValidate runs `nginx -t`. If `path` is non-empty it's passed as -c.
func defaultValidate(path string) error {
	args := []string{"-t"}
	if path != "" {
		args = append(args, "-c", path)
	}
	cmd := exec.Command("nginx", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nginx -t: %w (%s)", err, stderr.String())
	}
	return nil
}

// DryRunWriter writes both rendered files to w instead of disk, each
// prefixed with a header marking which path it would land at. Used by
// `apigw install --dry-run` to show the full intended state.
func (m *Manager) DryRunWriter(cfg *config.Config, w io.Writer) error {
	serverBody, httpBody, err := m.gen.Render(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(w, "# would write to %s @ %s\n", m.sitePath, now); err != nil {
		return err
	}
	if _, err := w.Write(serverBody); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n# would write to %s @ %s\n", HTTPConfPath, now); err != nil {
		return err
	}
	if _, err := w.Write(httpBody); err != nil {
		return err
	}
	return nil
}
