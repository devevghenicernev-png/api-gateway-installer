package deploy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
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

	cl, err := Clone(ctx, CloneRequest{
		Name:   req.Name,
		Repo:   req.Repo,
		Branch: req.Branch,
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
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for i := 0; i < 20 && time.Now().Before(deadline); i++ {
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
