package serve

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/dashboard"
)

// runSecurityReloader watches config.yaml for changes and pushes a fresh
// admin-token table + RBAC view into the running Security layer. This
// closes the "I revoked a token but it still works" UX gap — operators
// expect `apigw auth admin token revoke` to take effect immediately,
// not at the next dashboard restart.
//
// Uses fsnotify to coalesce burst writes (editors save via temp+rename
// which fires Create AND Write events; we debounce to one reload per
// 500ms quiet window). Closes cleanly on ctx.Done.
//
// No reload happens if Security is nil (legacy mode) or the config
// path can't be resolved — both are non-fatal degradations, the
// dashboard just keeps the table it loaded at startup.
func runSecurityReloader(ctx context.Context, sec *dashboard.Security, logger *slog.Logger) {
	if sec == nil {
		return
	}
	cfg, err := reloadConfig()
	if err != nil {
		logger.Warn("security reloader: initial config load failed", "err", err)
		return
	}
	configPath := cfg.Path()
	if configPath == "" {
		return
	}
	watchDir := filepath.Dir(configPath)
	configBase := filepath.Base(configPath)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Warn("security reloader: fsnotify unavailable", "err", err)
		return
	}
	defer w.Close()
	// Watch the directory (not the file) because atomic-rename saves
	// remove the original inode; the watcher then attaches to nothing.
	if err := w.Add(watchDir); err != nil {
		logger.Warn("security reloader: watch dir failed", "dir", watchDir, "err", err)
		return
	}

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != configBase {
				continue
			}
			// Coalesce burst writes — editors save with multiple events.
			debounce.Reset(500 * time.Millisecond)
		case err := <-w.Errors:
			logger.Warn("security reloader: fsnotify error", "err", err)
		case <-debounce.C:
			fresh, lerr := reloadConfig()
			if lerr != nil {
				logger.Warn("security reloader: reload failed", "err", lerr)
				continue
			}
			sec.ReloadTokens(fresh.Security)
			logger.Info("security reloaded from config",
				slog.Int("tokens", len(fresh.Security.AdminTokens)),
				slog.Int("assignments", len(fresh.Security.Assignments)),
				slog.Bool("rbac_enforce", fresh.Security.RBACEnforce))
			if sec.Alerts != nil {
				sec.FireAlert("security.reloaded", "info",
					"Security config reloaded",
					"Token table and RBAC assignments refreshed from disk.",
					"security")
			}
			_ = config.Defaults // keep import alive if no other use
		}
	}
}
