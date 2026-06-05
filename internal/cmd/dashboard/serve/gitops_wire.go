package serve

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/config"
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
// reload-config is dirty (the long-running dashboard sees it on next
// tick).
//
// Before the copy we diff the upstream YAML against the live config and
// emit one audit/alert event per detected change. This gives operators
// visibility when GitOps removes an entry they just added via CLI (the
// "GitOps source of truth" trade-off) — without the diff, CLI changes
// silently revert on next poll.
//
// Cascade-delete is implicit: a full file overwrite removes anything
// the upstream YAML doesn't list. The diff above makes it explicit and
// audit-traceable.
func runGitOps(ctx context.Context, gc gitopsCfg, logger *slog.Logger, sec *dashboard.Security) {
	apply := func(root string) error {
		src := filepath.Join(root, "config.yaml")
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("gitops: %s missing in repo path", src)
		}
		liveCfg, err := reloadConfig()
		if err != nil {
			return err
		}
		dst := liveCfg.Path()

		// Diff upstream vs live so we can audit cascade-deletes.
		upstreamCfg, derr := config.LoadFrom(src)
		if derr != nil {
			logger.Warn("gitops: parse upstream failed", "err", derr)
		} else if uc, ok := upstreamCfg.(*config.Config); ok && sec != nil {
			diffGitOps(liveCfg, uc, sec, logger)
		}

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

// diffGitOps emits one alert per (added, removed) entry between the
// upstream and live configs. Doesn't audit-log directly — runGitOps
// has no operator identity to attribute, but the alerts dispatcher
// covers PagerDuty/Slack/email which is what on-call sees.
func diffGitOps(live, upstream *config.Config, sec *dashboard.Security, logger *slog.Logger) {
	liveAPIs := map[string]struct{}{}
	for _, a := range live.APIs {
		liveAPIs[a.Name] = struct{}{}
	}
	upAPIs := map[string]struct{}{}
	for _, a := range upstream.APIs {
		upAPIs[a.Name] = struct{}{}
	}
	for name := range liveAPIs {
		if _, ok := upAPIs[name]; !ok {
			logger.Warn("gitops will remove api", "name", name)
			sec.FireAlert("gitops.cascade_delete", "warning",
				"GitOps removed API: "+name,
				"The next reconcile will delete API "+name+" because the upstream YAML no longer lists it.",
				"api/"+name)
		}
	}
	for name := range upAPIs {
		if _, ok := liveAPIs[name]; !ok {
			logger.Info("gitops will add api", "name", name)
			sec.FireAlert("gitops.cascade_add", "info",
				"GitOps added API: "+name, "", "api/"+name)
		}
	}
	liveDeploys := map[string]struct{}{}
	for _, d := range live.Deploys {
		liveDeploys[d.Name] = struct{}{}
	}
	upDeploys := map[string]struct{}{}
	for _, d := range upstream.Deploys {
		upDeploys[d.Name] = struct{}{}
	}
	for name := range liveDeploys {
		if _, ok := upDeploys[name]; !ok {
			logger.Warn("gitops will remove deploy", "name", name)
			sec.FireAlert("gitops.cascade_delete", "warning",
				"GitOps removed deploy: "+name,
				"The next reconcile will delete deploy "+name+" because the upstream YAML no longer lists it.",
				"deploy/"+name)
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
