package serve

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/dashboard"
	"github.com/devevghenicernev-png/apigw/internal/gitops"
)

// gitopsCfg is the resolved config block passed into the reconciler.
type gitopsCfg struct {
	RepoURL   string
	Branch    string
	Path      string
	Interval  time.Duration
	HTTPToken string
	SSHKey    string
	SSHPass   string
}

// gitopsLogger adapts *slog.Logger to the gitops.Logger interface.
type gitopsLogger struct{ l *slog.Logger }

func (g gitopsLogger) Info(msg string, fields ...any)  { g.l.Info(msg, fields...) }
func (g gitopsLogger) Warn(msg string, fields ...any)  { g.l.Warn(msg, fields...) }
func (g gitopsLogger) Error(msg string, fields ...any) { g.l.Error(msg, fields...) }

// runGitOps installs and drives the reconciler. apply() copies
// <checkout>/<cfg.Path>/config.yaml onto the live config path, then
// reload-config is dirty (the long-running dashboard sees it on next tick).
//
// Every reconcile fires an alert on success or failure when alerts are
// configured. The Security audit log records the apply via Action
// "gitops.reconcile".
func runGitOps(ctx context.Context, gc gitopsCfg, logger *slog.Logger, sec *dashboard.Security) {
	apply := func(root string) error {
		src := filepath.Join(root, "config.yaml")
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("gitops: %s missing in repo path", src)
		}
		cfg, err := reloadConfig()
		if err != nil {
			return err
		}
		dst := cfg.Path()
		// Atomic via temp+rename
		tmp := dst + ".gitops.new"
		if err := copyFile(src, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if sec != nil {
			sec.FireAlert("gitops.applied", "info",
				"GitOps config reconciled",
				fmt.Sprintf("Successfully applied %s from gitops repo.", dst),
				"gitops")
		}
		logger.Info("gitops applied", "path", dst)
		return nil
	}

	rec := gitops.New(gitops.Config{
		RepoURL:    gc.RepoURL,
		Branch:     gc.Branch,
		Path:       gc.Path,
		Interval:   gc.Interval,
		HTTPToken:  gc.HTTPToken,
		SSHKeyFile: gc.SSHKey,
		SSHKeyPass: gc.SSHPass,
	}, apply, gitopsLogger{logger})

	if err := rec.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Warn("gitops reconciler exited", "err", err)
		if sec != nil {
			sec.FireAlert("gitops.failed", "warning",
				"GitOps reconciler stopped",
				"The gitops poller exited with an error: "+err.Error(),
				"gitops")
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
