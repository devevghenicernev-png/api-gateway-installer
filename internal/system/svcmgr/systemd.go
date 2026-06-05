package svcmgr

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/assets"
	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// systemdManager implements Manager via systemctl + unit files in
// /etc/systemd/system. It is the reference implementation; launchd and
// OpenRC strive for semantic parity but inevitably differ in detail.
type systemdManager struct {
	unitDir string
}

func newSystemd() Manager {
	return &systemdManager{unitDir: paths.SystemdUnitDir()}
}

func (s *systemdManager) Kind() Kind { return Systemd }

// ---------- high-level installs ----------

func (s *systemdManager) InstallWebhookService(binPath, addr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9000"
	}
	body, err := renderSystemdTemplate("systemd/apigw-webhook.service.tmpl", map[string]any{
		"BinPath": binPath,
		"Addr":    addr,
	})
	if err != nil {
		return fmt.Errorf("render webhook unit: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.unitDir, "apigw-webhook.service"), body, 0o644); err != nil {
		return err
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return runCmd("systemctl", "enable", "--now", "apigw-webhook.service")
}

func (s *systemdManager) UninstallWebhookService() error {
	_ = runCmd("systemctl", "disable", "--now", "apigw-webhook.service")
	_ = os.Remove(filepath.Join(s.unitDir, "apigw-webhook.service"))
	return s.DaemonReload()
}

func (s *systemdManager) InstallDashboardService(binPath, addr, webhookAddr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9080"
	}
	if webhookAddr == "" {
		webhookAddr = ":9000"
	}
	body, err := renderSystemdTemplate("systemd/apigw-dashboard.service.tmpl", map[string]any{
		"BinPath":     binPath,
		"Addr":        addr,
		"WebhookAddr": webhookAddr,
	})
	if err != nil {
		return fmt.Errorf("render dashboard unit: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.unitDir, "apigw-dashboard.service"), body, 0o644); err != nil {
		return err
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return runCmd("systemctl", "enable", "--now", "apigw-dashboard.service")
}

func (s *systemdManager) UninstallDashboardService() error {
	_ = runCmd("systemctl", "disable", "--now", "apigw-dashboard.service")
	_ = os.Remove(filepath.Join(s.unitDir, "apigw-dashboard.service"))
	return s.DaemonReload()
}

func (s *systemdManager) InstallTLSRenewTimer(binPath string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	files := map[string]string{
		"apigw-tls-renew.service": "systemd/apigw-tls-renew.service.tmpl",
		"apigw-tls-renew.timer":   "systemd/apigw-tls-renew.timer.tmpl",
	}
	for name, src := range files {
		body, err := renderSystemdTemplate(src, map[string]any{"BinPath": binPath})
		if err != nil {
			return fmt.Errorf("render %s: %w", name, err)
		}
		if err := atomicWriteFile(filepath.Join(s.unitDir, name), body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return runCmd("systemctl", "enable", "--now", "apigw-tls-renew.timer")
}

func (s *systemdManager) UninstallTLSRenewTimer() error {
	_ = runCmd("systemctl", "disable", "--now", "apigw-tls-renew.timer")
	for _, name := range []string{"apigw-tls-renew.timer", "apigw-tls-renew.service"} {
		_ = os.Remove(filepath.Join(s.unitDir, name))
	}
	return s.DaemonReload()
}

func (s *systemdManager) InstallDeployService(spec DeploySpec) error {
	// First install the template once; cheap to re-run.
	body, err := renderSystemdTemplate("systemd/apigw-deploy@.service.tmpl", map[string]string{
		"User":  spec.User,
		"Group": spec.Group,
	})
	if err != nil {
		return fmt.Errorf("render template: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(s.unitDir, "apigw-deploy@.service"), body, 0o644); err != nil {
		return fmt.Errorf("write template: %w", err)
	}
	if err := s.DaemonReload(); err != nil {
		return err
	}
	return s.WriteDeployOverride(spec.Name, spec.Port, spec.EnvFile)
}

func (s *systemdManager) UninstallDeployService(name string) error {
	unit := "apigw-deploy@" + name + ".service"
	_ = runCmd("systemctl", "disable", "--now", unit)
	dropinDir := filepath.Join(s.unitDir, "apigw-deploy@"+name+".service.d")
	_ = os.RemoveAll(dropinDir)
	return s.DaemonReload()
}

func (s *systemdManager) WriteDeployOverride(name string, _ int, _ string) error {
	dropinDir := filepath.Join(s.unitDir, "apigw-deploy@"+name+".service.d")
	if err := os.MkdirAll(dropinDir, 0o755); err != nil {
		return fmt.Errorf("mkdir override dir: %w", err)
	}
	body := fmt.Sprintf("# apigw-managed drop-in for %s. Generated %s.\n"+
		"# Add per-deploy overrides here (CPUQuota, extra env, etc.).\n"+
		"[Service]\n",
		name, time.Now().UTC().Format(time.RFC3339))
	return atomicWriteFile(filepath.Join(dropinDir, "override.conf"), []byte(body), 0o644)
}

// ---------- lifecycle ----------

func (s *systemdManager) Start(name string) error {
	return runCmd("systemctl", "start", systemdUnit(name))
}
func (s *systemdManager) Stop(name string) error {
	return runCmd("systemctl", "stop", systemdUnit(name))
}
func (s *systemdManager) Restart(name string) error {
	return runCmd("systemctl", "restart", systemdUnit(name))
}
func (s *systemdManager) Reload(name string) error {
	return runCmd("systemctl", "reload", systemdUnit(name))
}
func (s *systemdManager) Enable(name string) error {
	return runCmd("systemctl", "enable", systemdUnit(name))
}
func (s *systemdManager) Disable(name string) error {
	return runCmd("systemctl", "disable", systemdUnit(name))
}

func (s *systemdManager) IsActive(name string) bool {
	return runCmd("systemctl", "is-active", "--quiet", systemdUnit(name)) == nil
}

func (s *systemdManager) Status(name string) Status {
	st := Status{}
	st.Active = s.IsActive(name)
	out, _ := exec.Command("systemctl", "is-enabled", "--quiet", systemdUnit(name)).Output()
	st.Enabled = len(out) == 0 // is-enabled exits 0 with no output when enabled
	if !st.Active {
		st.Detail = "inactive"
	} else if !st.Enabled {
		st.Detail = "active (disabled)"
	} else {
		st.Detail = "active"
	}
	return st
}

func (s *systemdManager) RunTimer(name string) error {
	return runCmd("systemctl", "start", systemdUnit(name))
}

func (s *systemdManager) DaemonReload() error { return runCmd("systemctl", "daemon-reload") }

// ---------- helpers ----------

// systemdUnit appends ".service" or ".timer" if needed. Callers pass logical
// names ("apigw-webhook", "apigw-tls-renew.timer"); we normalize.
func systemdUnit(name string) string {
	if strings.HasSuffix(name, ".service") || strings.HasSuffix(name, ".timer") {
		return name
	}
	// Bare names get ".service" — timers must pass the full name explicitly.
	return name + ".service"
}

// renderSystemdTemplate parses one template from the assets FS and executes
// it with `data`.
func renderSystemdTemplate(path string, data any) ([]byte, error) {
	tmpl, err := template.ParseFS(assets.Systemd(), path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// runCmd is the shared exec wrapper — inherit stderr, return on non-zero.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, stderr.String())
	}
	return nil
}

// atomicWriteFile writes b to path atomically — temp file in the same dir,
// fsync, rename. Shared by all impls.
func atomicWriteFile(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// defaultBinPath is the path to the apigw binary that ExecStart fields point
// at. We resolve via os.Executable() so a binary installed under /opt or
// $HOME/bin keeps working.
func defaultBinPath() string {
	p, err := os.Executable()
	if err != nil {
		return "/usr/local/bin/apigw"
	}
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return abs
}
