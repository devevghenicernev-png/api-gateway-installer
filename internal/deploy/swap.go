package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/system"
)

// ApplyResult is the outcome of a single deploy pass. Empty SHA means we
// detected the request was redundant (same commit as current); in that case
// Skipped=true and no other fields are populated.
type ApplyResult struct {
	Name     string
	SHA      string
	Skipped  bool
	Duration time.Duration
}

// Apply runs the full deploy sequence for a single deployment.
//
// Sequence (Phase 3 v1):
//  1. Clone repo at branch → releases/<sha>/ (idempotent — skip if SHA matches current).
//  2. Detect runtime + write .apigw/start.sh + run build.
//  3. Atomic flip: current → releases/<sha>.
//  4. systemctl restart apigw-deploy@<name>.
//  5. Health probe on the deploy port (20 × 500 ms).
//  6. On failure: revert the symlink, restart old release, return error.
//
// True zero-downtime (two-port upstream swap per ARCHITECTURE.md
// §"Zero-downtime") is Phase 3.1 — needs socket activation or N+1 port
// allocation. v1 has a brief downtime window between `systemctl restart`
// and the new instance binding (~200-2000 ms typical).
type ApplyRequest struct {
	Name        string
	Repo        string
	Branch      string
	Port        int
	RuntimeHint string
	Build       string // optional override
	Start       string // optional override
	Logsink     io.Writer
	ForceClone  bool // ignore SHA-match short-circuit

	// HealthPath, when non-empty, is requested via HTTP GET against
	// http://127.0.0.1:<port><HealthPath> as a strict probe — a 2xx is
	// required for the deploy to be considered alive. Default behavior
	// (empty) keeps the legacy lenient TCP-only check so existing apps
	// without a /health endpoint don't regress.
	HealthPath string

	// Shared lists release-relative paths that must persist across
	// releases (uploads/, SQLite files, log dirs the app writes to
	// directly). See config.Deploy.Shared + SharedDir().
	Shared []string

	// Metrics, when non-nil, receives Apply() count + duration.
	Metrics ApplyMetrics
}

// ApplyMetrics is the minimal surface Apply depends on. Lets the deploy
// package stay clear of internal/metrics imports (avoids a cycle once any
// downstream consumer pulls deploy in).
type ApplyMetrics interface {
	IncDeployApply(deploy, status string)
	ObserveDeployApplyDuration(deploy string, seconds float64)
}

func Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	start := time.Now()
	res := ApplyResult{Name: req.Name}
	// Status label set by every return path. Default "failed"; success
	// paths overwrite to "ok" or "skipped" before the deferred Inc lands.
	status := "failed"
	defer func() {
		if req.Metrics != nil {
			req.Metrics.IncDeployApply(req.Name, status)
			req.Metrics.ObserveDeployApplyDuration(req.Name, time.Since(start).Seconds())
		}
	}()

	// Ensure the single apigw deploy user exists before anything depends on
	// it: Clone chowns the release dir to it, the build runs as it, and the
	// runtime systemd unit runs as it. On a fresh install it doesn't exist
	// yet, and a silent lookup failure inside chownTree would leave the
	// release owned by root → first build fails with EACCES. Cheap +
	// idempotent so we don't gate on runtime hint.
	_ = system.EnsureSystemUser(RunUser)

	cl, err := Clone(ctx, CloneRequest{
		Name:   req.Name,
		Repo:   req.Repo,
		Branch: req.Branch,
		Force:  req.ForceClone,
	})
	if err != nil {
		return res, fmt.Errorf("clone: %w", err)
	}
	res.SHA = cl.SHA

	// Short-circuit if symlink already points at this SHA.
	if !req.ForceClone {
		if cur, _ := os.Readlink(CurrentSymlink(req.Name)); filepath.Base(cur) == cl.SHA {
			res.Skipped = true
			res.Duration = time.Since(start)
			status = "skipped"
			return res, nil
		}
	}

	det, err := Resolve(req.RuntimeHint, cl.Path)
	if err != nil {
		return res, fmt.Errorf("detect runtime: %w", err)
	}

	build, startCmd := DefaultsFor(det.Runtime)
	if req.Build != "" {
		build = req.Build
	}
	if req.Start != "" {
		startCmd = req.Start
	}
	spec := BuildSpec{
		Runtime:     det.Runtime,
		Build:       build,
		Start:       startCmd,
		Port:        req.Port,
		EnvFilePath: EnvFile(req.Name),
	}
	if _, err := PrepareRelease(ctx, req.Name, spec, cl.Path, req.Logsink); err != nil {
		return res, fmt.Errorf("prepare release: %w", err)
	}

	// No post-build chown: Clone already chowned the whole tree to the
	// deploy user, and the build ran as that same user, so the runtime unit
	// (also that user) can write into uploads/, logs, SQLite files, etc.
	// The only root-owned files are .apigw/{start.sh,build.log}, which the
	// runtime reads/executes but never writes.

	// Materialise shared/ paths. linkSharedPaths chowns the persistent dirs
	// to the deploy user (they're created by the root worker), so migrated
	// first-run content ends up owned by the runtime user.
	if err := linkSharedPaths(req.Name, cl.Path, req.Shared, req.Logsink); err != nil {
		return res, fmt.Errorf("shared dirs: %w", err)
	}

	previous, _ := os.Readlink(CurrentSymlink(req.Name)) // empty on first deploy

	// Ensure the systemd template + per-deploy override exist before we try
	// to start the unit. Cheap to call repeatedly.
	if err := InstallTemplateUnit(); err != nil {
		return res, fmt.Errorf("install systemd template: %w", err)
	}
	if err := WriteOverride(req.Name); err != nil {
		return res, fmt.Errorf("write override: %w", err)
	}
	if err := EnsureEnvFile(req.Name); err != nil {
		return res, fmt.Errorf("ensure env file: %w", err)
	}

	if err := flipSymlink(CurrentSymlink(req.Name), cl.Path); err != nil {
		return res, fmt.Errorf("symlink flip: %w", err)
	}

	if err := Enable(req.Name); err != nil {
		rollback(req.Name, previous)
		return res, fmt.Errorf("systemctl enable: %w", err)
	}
	if err := Restart(req.Name); err != nil {
		rollback(req.Name, previous)
		return res, fmt.Errorf("systemctl restart: %w", err)
	}

	if err := healthProbe(ctx, req.Port, req.HealthPath); err != nil {
		rollback(req.Name, previous)
		_ = Restart(req.Name)
		return res, fmt.Errorf("health probe (rolled back): %w", err)
	}

	// Best-effort cleanup of release dirs older than the 5 most recent.
	_ = PruneOldReleases(req.Name, 5)

	res.Duration = time.Since(start)
	status = "ok"
	return res, nil
}

// linkSharedPaths wires each entry in `shared` (release-root-relative) into
// a per-deploy persistent directory under SharedDir(name), so app-written
// state survives across releases.
//
// For each `p` in shared:
//  1. Ensure <shared>/<p> exists (mkdir + chown RunUser).
//  2. If <release>/<p> exists AND is not already a symlink: this is the
//     first deploy with `shared` set for this path. Copy its contents into
//     <shared>/<p> (so we don't lose what the operator's app has been
//     writing into the release tree), then rm -rf <release>/<p>.
//  3. Symlink <release>/<p> → <shared>/<p>.
//
// Idempotent: subsequent deploys just see no <release>/<p> (it never
// existed in the git checkout once shared/ was set up) and create the
// symlink. If the operator removes a path from `shared`, the
// <shared>/<p> tree is left intact on disk for safety — they can delete
// it by hand.
func linkSharedPaths(name, releasePath string, shared []string, sink io.Writer) error {
	if len(shared) == 0 {
		return nil
	}
	sharedRoot := SharedDir(name)
	for _, raw := range shared {
		p := filepath.Clean(strings.TrimSpace(raw))
		if p == "" || p == "." || p == "/" || strings.HasPrefix(p, "..") || strings.HasPrefix(p, "/") {
			if sink != nil {
				fmt.Fprintf(sink, "warn: skipping invalid shared path %q (must be a relative path inside the release)\n", raw)
			}
			continue
		}
		sharedPath := filepath.Join(sharedRoot, p)
		releaseTarget := filepath.Join(releasePath, p)

		if err := os.MkdirAll(sharedPath, 0o755); err != nil {
			return fmt.Errorf("mkdir shared %s: %w", sharedPath, err)
		}
		// Best-effort chown so the runtime user can read+write the
		// persistent dir from inside the release.
		_ = chownTree(sharedPath, RunUser)

		// If the release dir already has this path (first deploy with
		// the entry, or git tracks it), migrate its contents into shared
		// and then replace it with a symlink.
		st, err := os.Lstat(releaseTarget)
		switch {
		case err == nil && st.Mode()&os.ModeSymlink != 0:
			// Already a symlink (left from a previous deploy). Remove
			// and recreate to make sure it points at *our* shared dir.
			_ = os.Remove(releaseTarget)
		case err == nil:
			// Plain file/dir from the git checkout — migrate then drop.
			if err := mergeIntoShared(releaseTarget, sharedPath); err != nil {
				return fmt.Errorf("migrate %s → %s: %w", releaseTarget, sharedPath, err)
			}
			if err := os.RemoveAll(releaseTarget); err != nil {
				return fmt.Errorf("rm migrated %s: %w", releaseTarget, err)
			}
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("stat %s: %w", releaseTarget, err)
		}
		if err := os.MkdirAll(filepath.Dir(releaseTarget), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", releaseTarget, err)
		}
		if err := os.Symlink(sharedPath, releaseTarget); err != nil {
			return fmt.Errorf("symlink %s → %s: %w", releaseTarget, sharedPath, err)
		}
	}
	return nil
}

// mergeIntoShared copies every entry from `src` (a directory or file in the
// release tree) into `dst` (the persistent shared dir), preserving anything
// already in `dst`. We never overwrite — the persistent copy wins, since
// it's what the running app has been mutating. If `src` is a file, it's
// treated as a single-entry sibling.
func mergeIntoShared(src, dst string) error {
	sInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !sInfo.IsDir() {
		// A plain file in the checkout — only copy if dst doesn't have
		// it yet (treat dst as a directory holding it).
		base := filepath.Base(src)
		out := filepath.Join(dst, base)
		if _, err := os.Stat(out); err == nil {
			return nil
		}
		return copyFile(src, out)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode().Perm())
		}
		if _, err := os.Lstat(out); err == nil {
			return nil // preserve persistent copy
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		}
		return copyFile(path, out)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	si, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, si.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// flipSymlink atomically points `link` at `target`.
//
// Linux symlink(2) is atomic; the trick is that os.Symlink doesn't replace
// an existing symlink. We use the standard "create-then-rename" idiom: make
// a temp symlink in the same directory, then rename(2) onto the live name.
func flipSymlink(link, target string) error {
	dir := filepath.Dir(link)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func rollback(name, previous string) {
	if previous == "" {
		return
	}
	_ = flipSymlink(CurrentSymlink(name), previous)
}

// healthProbe loops 20 times with 500 ms backoff trying a TCP connect to
// 127.0.0.1:<port>. If `port` is 0 we treat the deploy as a worker
// (no listener) and return nil immediately.
//
// When `healthPath` is non-empty, the probe is STRICT: a successful TCP
// connect is not enough — we additionally require GET http://127.0.0.1:<port><healthPath>
// to return 2xx. Apps that 404 on / can no longer slip through as "alive"
// when they opt in (deploy config sets `health_path: /health`).
//
// When `healthPath` is empty we fall back to the legacy lenient behaviour
// (TCP only + best-effort GET /health whose status is ignored). This keeps
// existing apps without a /health endpoint from regressing.
func healthProbe(ctx context.Context, port int, healthPath string) error {
	if port == 0 {
		return nil
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	// 30s deadline accommodates frameworks with non-trivial boot cost
	// (Payload CMS + MongoDB connection ~9-12s on ARM, Rails / Spring
	// apps similar). The earlier 10s was tight enough to race a healthy
	// startup and roll back a perfectly fine deploy.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for i := 0; i < 60 && time.Now().Before(deadline); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 750*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if healthPath != "" {
				if herr := strictHealthURL(ctx, port, healthPath); herr != nil {
					lastErr = herr
					time.Sleep(500 * time.Millisecond)
					continue
				}
				return nil
			}
			// Lenient legacy mode: TCP alive + best-effort GET /health
			// whose status is intentionally ignored.
			_ = tryHealthURL(ctx, port)
			return nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nothing listening on %s", addr)
	}
	return lastErr
}

// strictHealthURL requires HTTP 2xx from http://127.0.0.1:<port><path>.
// Used when the deploy config opts in via `health_path`.
func strictHealthURL(ctx context.Context, port int, path string) error {
	c := &http.Client{Timeout: 2 * time.Second}
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s returned %d (want 2xx)", url, resp.StatusCode)
	}
	return nil
}

func tryHealthURL(ctx context.Context, port int) error {
	c := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
