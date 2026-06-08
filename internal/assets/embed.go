// Package assets exposes embedded template and static-asset bytes.
//
// Templates are read at package init time (via go:embed). The FS handle is
// re-exported as Templates so consumers don't depend on the embed package
// directly — easier to swap for an afero.Fs in tests if we ever need to.
package assets

import "embed"

//go:embed nginx/*.tmpl
var nginxFS embed.FS

//go:embed systemd/*.tmpl
var systemdFS embed.FS

//go:embed launchd/*.tmpl
var launchdFS embed.FS

//go:embed openrc/*.tmpl
var openrcFS embed.FS

//go:embed all:dashboard
var dashboardFS embed.FS

//go:embed grafana/*.json
var grafanaFS embed.FS

// Nginx returns the embedded nginx-template filesystem.
func Nginx() embed.FS { return nginxFS }

// Systemd returns the embedded systemd-unit-template filesystem.
func Systemd() embed.FS { return systemdFS }

// Launchd returns the embedded launchd plist-template filesystem.
func Launchd() embed.FS { return launchdFS }

// OpenRC returns the embedded OpenRC init-script template filesystem.
func OpenRC() embed.FS { return openrcFS }

// Dashboard returns the embedded static UI filesystem (HTML+CSS+JS).
//
// Files live under dashboard/ in the embedded FS so the dashboard server can
// `fs.Sub(Dashboard(), "dashboard")` to strip the prefix.
func Dashboard() embed.FS { return dashboardFS }

// Grafana returns the embedded Grafana dashboard JSON files. Used by
// the `apigw dashboard grafana` command to print importable JSON.
func Grafana() embed.FS { return grafanaFS }
