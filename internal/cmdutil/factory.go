package cmdutil

import (
	"log/slog"

	"github.com/devevghenicernev-png/apigw/internal/iostreams"
	"github.com/spf13/afero"
)

// Factory is the dependency-injection container threaded through every
// subcommand. Eager fields are constructed once in apigwcmd.Main and shared.
// Lazy fields are functions so commands that don't need them (e.g. `version`)
// pay no cost — and a broken config doesn't kill `apigw version`.
//
// Mirror of cli/cli/pkg/cmdutil.Factory.
type Factory struct {
	// Identity / build info
	AppVersion string
	Commit     string
	BuildDate  string

	// Eager — constructed in Main() before any command runs.
	IOStreams *iostreams.IOStreams
	Logger    *slog.Logger
	Prompter  Prompter
	FS        afero.Fs // afero.NewOsFs in production, MemMapFs in tests

	// LogLevel mirrors the Logger's current level. Set by root's
	// PersistentPreRunE after parsing --verbose/--debug.
	LogLevel slog.Level

	// RebuildLogger lets PersistentPreRunE replace Factory.Logger with a
	// fresh handler at the chosen level. Wired by apigwcmd.Main(); commands
	// shouldn't call it directly.
	RebuildLogger func(slog.Level) *slog.Logger

	// ConfigPath overrides the search list when --config is passed.
	// Empty = default (XDG → /etc/apigw/config.yaml). Read by the lazy
	// Config loader.
	ConfigPath string

	// Lazy — invoked by the commands that need them. Errors here MUST be
	// returned, never panicked. `version` and `help` work even if these fail.
	Config  func() (Config, error)
	Nginx   func() (Nginx, error)
	Systemd func() (Systemd, error)
	Events  func() (Events, error)
}

// Config / Nginx / Systemd / Events are forward-declared as interfaces so
// cmdutil doesn't depend on the concrete packages — avoids import cycles.
// The concrete types live in internal/config, internal/nginx, etc., and
// satisfy these interfaces implicitly.

type Config interface {
	Path() string
	Save() error
	Snapshot() ([]byte, error) // for backup
}

type Nginx interface {
	Validate() error          // nginx -t
	Reload() error            // systemctl reload nginx
	Render() ([]byte, error)  // generate config bytes, no side effects
	WriteAndReload() error    // atomic: write + validate + reload + rollback
}

type Systemd interface {
	Reload() error                                     // daemon-reload
	Start(unit string) error
	Stop(unit string) error
	Restart(unit string) error
	Enable(unit string) error
	IsActive(unit string) (bool, error)
}

type Events interface {
	Publish(topic, evType string, data []byte)
	Subscribe(topics []string, sinceID uint64) (<-chan any, func())
}
