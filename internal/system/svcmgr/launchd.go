package svcmgr

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/devevghenicernev-png/apigw/internal/assets"
	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// launchdManager implements Manager via launchctl + plist files in
// /Library/LaunchDaemons (system-wide) or ~/Library/LaunchAgents (per-user).
//
// We default to /Library/LaunchDaemons because production apigw needs to
// start at boot, before any user is logged in. Operators in dev mode who
// don't want sudo can override via APIGW_LAUNCHD_DAEMON_DIR=$HOME/Library/LaunchAgents.
type launchdManager struct {
	plistDir string
	stateDir string
	logDir   string
	cfgDir   string
}

func newLaunchd() Manager {
	dir := paths.LaunchdDaemonDir()
	// If we can't write to /Library/LaunchDaemons (no sudo), fall back to
	// per-user LaunchAgents. This lets `apigw dashboard start` work for a
	// non-admin dev without prompting for an admin password.
	if !canWrite(dir) {
		dir = paths.LaunchdAgentDir()
	}
	return &launchdManager{
		plistDir: dir,
		stateDir: paths.StateDir(),
		logDir:   paths.LogDir(),
		cfgDir:   paths.ConfigDir(),
	}
}

func (l *launchdManager) Kind() Kind { return Launchd }

// ---------- high-level installs ----------

func (l *launchdManager) InstallWebhookService(binPath, addr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9000"
	}
	if err := l.ensureLogDir(); err != nil {
		return err
	}
	return l.installPlist("apigw-webhook", "launchd/apigw-webhook.plist.tmpl", map[string]any{
		"BinPath":   binPath,
		"Addr":      addr,
		"LogDir":    l.logDir,
		"StateDir":  l.stateDir,
		"ConfigDir": l.cfgDir,
	})
}

func (l *launchdManager) UninstallWebhookService() error {
	return l.uninstallPlist("apigw-webhook")
}

func (l *launchdManager) InstallDashboardService(binPath, addr, webhookAddr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9080"
	}
	if webhookAddr == "" {
		webhookAddr = ":9000"
	}
	if err := l.ensureLogDir(); err != nil {
		return err
	}
	return l.installPlist("apigw-dashboard", "launchd/apigw-dashboard.plist.tmpl", map[string]any{
		"BinPath":     binPath,
		"Addr":        addr,
		"WebhookAddr": webhookAddr,
		"LogDir":      l.logDir,
		"StateDir":    l.stateDir,
		"ConfigDir":   l.cfgDir,
	})
}

func (l *launchdManager) UninstallDashboardService() error {
	return l.uninstallPlist("apigw-dashboard")
}

func (l *launchdManager) InstallTLSRenewTimer(binPath string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if err := l.ensureLogDir(); err != nil {
		return err
	}
	return l.installPlist("apigw-tls-renew", "launchd/apigw-tls-renew.plist.tmpl", map[string]any{
		"BinPath":   binPath,
		"LogDir":    l.logDir,
		"StateDir":  l.stateDir,
		"ConfigDir": l.cfgDir,
	})
}

func (l *launchdManager) UninstallTLSRenewTimer() error {
	return l.uninstallPlist("apigw-tls-renew")
}

func (l *launchdManager) InstallDeployService(spec DeploySpec) error {
	if err := l.ensureLogDir(); err != nil {
		return err
	}
	return l.installPlist("apigw-deploy-"+spec.Name, "launchd/apigw-deploy.plist.tmpl", map[string]any{
		"Name":    spec.Name,
		"WorkDir": spec.WorkDir,
		"Port":    fmt.Sprint(spec.Port),
		"LogDir":  l.logDir,
		"Env":     map[string]string{}, // populated from EnvironmentFile on Linux; macOS reads via /bin/sh start.sh
	})
}

func (l *launchdManager) UninstallDeployService(name string) error {
	return l.uninstallPlist("apigw-deploy-" + name)
}

// WriteDeployOverride: launchd has no drop-in mechanism. We re-install the
// plist with new values. Callers pass nothing meaningful here so this is a
// no-op; the actual per-deploy values come via InstallDeployService.
func (l *launchdManager) WriteDeployOverride(name string, _ int, _ string) error {
	return nil
}

// ---------- lifecycle ----------

func (l *launchdManager) Start(name string) error {
	label := launchdLabel(name)
	plist := l.plistPath(name)
	// `launchctl load` (legacy) is sufficient on macOS 10.10+; on 11+ the
	// new `launchctl bootstrap` requires a domain target. We try the modern
	// form first, fall back to load.
	if err := runCmd("launchctl", "bootstrap", l.domain(), plist); err == nil {
		_ = runCmd("launchctl", "kickstart", l.domain()+"/"+label)
		return nil
	}
	return runCmd("launchctl", "load", plist)
}

func (l *launchdManager) Stop(name string) error {
	label := launchdLabel(name)
	if err := runCmd("launchctl", "bootout", l.domain()+"/"+label); err == nil {
		return nil
	}
	return runCmd("launchctl", "unload", l.plistPath(name))
}

func (l *launchdManager) Restart(name string) error {
	_ = l.Stop(name)
	return l.Start(name)
}

func (l *launchdManager) Reload(name string) error {
	// launchd has no SIGHUP convention; closest equivalent is stop+start.
	return l.Restart(name)
}

func (l *launchdManager) Enable(name string) error {
	// `RunAtLoad=true` in the plist is what enables boot-time start; loading
	// the plist is "enable". No separate command.
	return l.Start(name)
}

func (l *launchdManager) Disable(name string) error {
	return l.Stop(name)
}

func (l *launchdManager) IsActive(name string) bool {
	label := launchdLabel(name)
	out, _ := exec.Command("launchctl", "list", label).Output()
	return len(out) > 0
}

func (l *launchdManager) Status(name string) Status {
	st := Status{}
	st.Active = l.IsActive(name)
	// "Enabled" on launchd = plist file exists (it will be auto-loaded at
	// next boot if RunAtLoad=true, which our templates all set).
	if _, err := os.Stat(l.plistPath(name)); err == nil {
		st.Enabled = true
	}
	if !st.Active && st.Enabled {
		st.Detail = "loaded but not running"
	} else if st.Active {
		st.Detail = "running"
	} else {
		st.Detail = "not loaded"
	}
	return st
}

func (l *launchdManager) RunTimer(name string) error {
	return runCmd("launchctl", "kickstart", "-k", l.domain()+"/"+launchdLabel(name))
}

func (l *launchdManager) DaemonReload() error {
	// launchd reads plists per-load; no global reload needed.
	return nil
}

// ---------- helpers ----------

func (l *launchdManager) installPlist(name, tmplPath string, data map[string]any) error {
	body, err := renderLaunchdTemplate(tmplPath, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	path := l.plistPath(name)
	if err := atomicWriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Load it now so it starts immediately. Best-effort — if launchctl
	// errors (already loaded), surface the error so caller can retry.
	return l.Start(name)
}

func (l *launchdManager) uninstallPlist(name string) error {
	_ = l.Stop(name)
	_ = os.Remove(l.plistPath(name))
	return nil
}

// plistPath returns the absolute file path for a logical service name.
// Naming: dev.apigw.<logical-name>.plist  (e.g. dev.apigw.webhook.plist)
func (l *launchdManager) plistPath(name string) string {
	return filepath.Join(l.plistDir, launchdLabel(name)+".plist")
}

// launchdLabel maps a logical apigw name to a reverse-DNS launchd label.
//
//	apigw-webhook        → dev.apigw.webhook
//	apigw-dashboard      → dev.apigw.dashboard
//	apigw-tls-renew      → dev.apigw.tls-renew
//	apigw-deploy-billing → dev.apigw.deploy.billing
func launchdLabel(name string) string {
	if name == "" {
		return "dev.apigw"
	}
	if len(name) > 6 && name[:7] == "apigw-d" && name[:13] == "apigw-deploy-" {
		return "dev.apigw.deploy." + name[len("apigw-deploy-"):]
	}
	if len(name) > 6 && name[:6] == "apigw-" {
		return "dev.apigw." + name[6:]
	}
	return "dev.apigw." + name
}

// domain returns the launchctl domain target — "system" when running as
// root (LaunchDaemons), "gui/<uid>" when running per-user (LaunchAgents).
func (l *launchdManager) domain() string {
	if l.plistDir == paths.LaunchdDaemonDir() {
		return "system"
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func (l *launchdManager) ensureLogDir() error {
	return os.MkdirAll(l.logDir, 0o755)
}

func renderLaunchdTemplate(path string, data any) ([]byte, error) {
	tmpl, err := template.ParseFS(assets.Launchd(), path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canWrite tries to create-and-remove a probe file in dir. Used to decide
// whether the launchd impl should target system daemons or user agents.
func canWrite(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	tmp, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return true
}
