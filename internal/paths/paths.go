// Package paths centralizes filesystem locations apigw reads or writes.
// Defaults vary by OS: Linux uses /etc and /var (FHS); macOS layers under
// Homebrew prefix (/usr/local on Intel, /opt/homebrew on Apple silicon).
//
// All paths can be overridden via APIGW_* environment variables — useful
// for tests, CI sandboxes, and operators who want non-standard layouts.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const (
	LinuxConfigDir         = "/etc/apigw"
	LinuxStateDir          = "/var/lib/apigw"
	LinuxLogDir            = "/var/log/apigw"
	LinuxNginxSitesAvail   = "/etc/nginx/sites-available"
	LinuxNginxSitesEnabled = "/etc/nginx/sites-enabled"
	LinuxNginxConfD        = "/etc/nginx/conf.d"
	LinuxSystemdUnits      = "/etc/systemd/system"
)

const (
	DarwinIntelPrefix = "/usr/local"
	DarwinARMPrefix   = "/opt/homebrew"
)

var (
	prefixOnce sync.Once
	cached     string
)

// DarwinPrefix detects Homebrew's prefix on macOS. Returns /opt/homebrew if
// that directory exists (Apple silicon default), else /usr/local. Memoized.
func DarwinPrefix() string {
	prefixOnce.Do(func() {
		if v := os.Getenv("HOMEBREW_PREFIX"); v != "" {
			cached = v
			return
		}
		if _, err := os.Stat(DarwinARMPrefix); err == nil {
			cached = DarwinARMPrefix
			return
		}
		cached = DarwinIntelPrefix
	})
	return cached
}

// envOr returns os.Getenv(key) if non-empty, else fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ConfigDir is where apigw.yaml + per-deploy .env files live.
func ConfigDir() string {
	return envOr("APIGW_CONFIG_DIR", osDefault(LinuxConfigDir, "etc/apigw"))
}

// StateDir holds release/ trees, jobs.db (bbolt), TLS certs, dashboard tokens.
func StateDir() string {
	return envOr("APIGW_STATE_DIR", osDefault(LinuxStateDir, "var/apigw"))
}

// LogDir is a fallback when no system journal is available.
func LogDir() string {
	return envOr("APIGW_LOG_DIR", osDefault(LinuxLogDir, "var/log/apigw"))
}

// NginxSitesAvailable is where apigw drops its generated server block.
func NginxSitesAvailable() string {
	return envOr("APIGW_NGINX_SITES_AVAILABLE", osDefault(LinuxNginxSitesAvail, "etc/nginx/servers"))
}

// NginxSitesEnabled is the symlink dir Debian-style nginx reads.
// On macOS/Homebrew layout there's no sites-enabled (servers/ is loaded directly),
// so we return NginxSitesAvailable() in that case — the manager's symlink
// step becomes a no-op.
func NginxSitesEnabled() string {
	if runtime.GOOS == "darwin" {
		return NginxSitesAvailable()
	}
	return envOr("APIGW_NGINX_SITES_ENABLED", LinuxNginxSitesEnabled)
}

// NginxConfD is where the http-context file lands (upstream blocks, gzip,
// rate-limit zones, CORS map).
func NginxConfD() string {
	return envOr("APIGW_NGINX_CONF_D", osDefault(LinuxNginxConfD, "etc/nginx/servers"))
}

// SystemdUnitDir is irrelevant on macOS/Alpine — svcmgr's launchd/openrc
// impls have their own destinations — but kept here for callers that still
// reference "the unit directory" in messages or doctor checks.
func SystemdUnitDir() string {
	return envOr("APIGW_SYSTEMD_UNIT_DIR", LinuxSystemdUnits)
}

// LaunchdAgentDir / LaunchdDaemonDir are macOS-specific. Daemons run as root
// at boot; agents run as the active user at login. apigw uses daemons for
// the gateway role and agents for dev-mode dashboard.
func LaunchdAgentDir() string {
	return envOr("APIGW_LAUNCHD_AGENT_DIR", filepath.Join(home(), "Library/LaunchAgents"))
}

func LaunchdDaemonDir() string {
	return envOr("APIGW_LAUNCHD_DAEMON_DIR", "/Library/LaunchDaemons")
}

// OpenRCInitDir is Alpine/Void's init script directory.
func OpenRCInitDir() string {
	return envOr("APIGW_OPENRC_INIT_DIR", "/etc/init.d")
}

// ReleaseDir returns the per-deploy release root: <StateDir>/releases/<name>.
func ReleaseDir(name string) string {
	return filepath.Join(StateDir(), "releases", name)
}

// EnvFile returns the per-deploy env file path.
func EnvFile(name string) string {
	return filepath.Join(ConfigDir(), name+".env")
}

// QueueDB is the bbolt path for the webhook job queue.
func QueueDB() string {
	return filepath.Join(StateDir(), "jobs.db")
}

// TLSCertDir is where lego writes account.json + cert+key.
func TLSCertDir() string {
	return filepath.Join(StateDir(), "tls")
}

// osDefault returns the Linux path on Linux, else the macOS-prefixed
// equivalent (e.g. /opt/homebrew/etc/apigw).
func osDefault(linuxPath, macSuffix string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(DarwinPrefix(), macSuffix)
	}
	return linuxPath
}

// home returns the current user's home directory; empty on read errors.
func home() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	return ""
}
