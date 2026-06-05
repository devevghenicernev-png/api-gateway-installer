// Package gitops keeps apigw's config in sync with a git repository.
//
// Pattern: operators commit YAML to a repo (e.g. github.com/acme/apigw-config).
// apigw polls the repo every Interval (default 30s) or accepts a push webhook,
// clones to a temp dir, diff'es against current state, and applies the changes
// via the same plan-then-apply path the CLI uses. Every reconcile is audited.
//
// Why not push-on-commit? Pull is more resilient: apigw can ride out github
// outages, the local config never drifts from main, and we don't need a
// long-lived webhook endpoint exposed to the internet (unless operator
// chooses).
//
// Conflict policy: git is the source of truth. Any local CLI changes that
// would conflict get reverted on next reconcile. Operators who want CLI
// edits to stick must commit them upstream first.
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Config is what the operator sets via `apigw gitops enable --repo …`.
type Config struct {
	RepoURL  string        // git URL (https or ssh)
	Branch   string        // default main
	Path     string        // sub-path inside repo containing apigw configs; default "apigw/"
	Interval time.Duration // poll cadence; default 30s

	// Auth.
	HTTPToken    string // GitHub/GitLab PAT (overrides ssh)
	SSHKeyFile   string // path to private SSH key
	SSHKeyPass   string // passphrase (optional)
	KnownHosts   string // path to known_hosts (defaults to apigw managed file)
}

// Reconciler is the long-running poller. One per apigw process.
type Reconciler struct {
	cfg     Config
	apply   func(workDir string) error // called with the checkout root after each fetch
	logger  Logger

	mu       sync.Mutex
	lastSHA  string
	stopCh   chan struct{}
}

// Logger is the minimal logging surface; satisfied by slog.Logger via
// adapter or zap, etc.
type Logger interface {
	Info(msg string, fields ...any)
	Warn(msg string, fields ...any)
	Error(msg string, fields ...any)
}

// New constructs a Reconciler. `apply` is invoked after each successful
// fetch with the checkout root; the caller is expected to:
//   1. Parse YAML files under <root>/<cfg.Path>
//   2. Diff against current apigw config
//   3. Apply changes via the same paths CLI mutations use
//   4. Audit-log the reconcile
func New(cfg Config, apply func(string) error, logger Logger) *Reconciler {
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Path == "" {
		cfg.Path = "apigw/"
	}
	return &Reconciler{cfg: cfg, apply: apply, logger: logger, stopCh: make(chan struct{})}
}

// Run blocks forever, polling cfg.RepoURL on cfg.Interval. Stop() cancels it.
// Caller typically: `go reconciler.Run(ctx)` from dashboard daemon.
func (r *Reconciler) Run(ctx context.Context) error {
	tick := time.NewTicker(r.cfg.Interval)
	defer tick.Stop()
	// First reconcile is immediate so we don't wait Interval after startup.
	if err := r.Once(ctx); err != nil {
		r.logger.Warn("gitops initial reconcile failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.stopCh:
			return nil
		case <-tick.C:
			if err := r.Once(ctx); err != nil {
				r.logger.Warn("gitops reconcile failed", "err", err)
			}
		}
	}
}

func (r *Reconciler) Stop() { close(r.stopCh) }

// Once performs a single reconcile cycle: fetch + (if SHA changed) apply.
// Exposed so tests + the `apigw gitops sync` CLI can drive it manually.
func (r *Reconciler) Once(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tmp, err := os.MkdirTemp("", "apigw-gitops-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	auth, _ := r.buildAuth()
	clone, err := gogit.PlainCloneContext(ctx, tmp, false, &gogit.CloneOptions{
		URL:           r.cfg.RepoURL,
		ReferenceName: plumbing.NewBranchReferenceName(r.cfg.Branch),
		SingleBranch:  true,
		Depth:         1,
		Auth:          auth,
	})
	if err != nil {
		return fmt.Errorf("gitops clone: %w", err)
	}
	head, err := clone.Head()
	if err != nil {
		return err
	}
	sha := head.Hash().String()
	if sha == r.lastSHA {
		return nil // no change
	}
	r.logger.Info("gitops change detected", "sha", sha[:12], "prev", trunc(r.lastSHA, 12))

	root := filepath.Join(tmp, r.cfg.Path)
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("gitops: %s missing in repo: %w", r.cfg.Path, err)
	}
	if err := r.apply(root); err != nil {
		return fmt.Errorf("gitops apply: %w", err)
	}
	r.lastSHA = sha
	r.logger.Info("gitops reconcile ok", "sha", sha[:12])
	return nil
}

func (r *Reconciler) buildAuth() (transport.AuthMethod, error) {
	if r.cfg.HTTPToken != "" {
		return &http.BasicAuth{Username: "apigw", Password: r.cfg.HTTPToken}, nil
	}
	// SSH support intentionally omitted in v0.5; HTTPS+PAT is dominant
	// (GitHub Apps, GitLab deploy tokens).
	return nil, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
