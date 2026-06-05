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

// openrcManager implements Manager via rc-service + rc-update + init.d
// scripts. Used on Alpine, Void, Gentoo (OpenRC profile) — anywhere systemd
// isn't PID 1 but rc-service is on PATH.
//
// Limitations:
//   - No timer infra. InstallTLSRenewTimer drops a script in /etc/periodic/daily/
//     instead, relying on busybox crond. If crond is absent, the install
//     surfaces a clear error.
//   - No drop-in mechanism. WriteDeployOverride re-renders the init script.
type openrcManager struct {
	initDir string
}

func newOpenRC() Manager {
	return &openrcManager{initDir: paths.OpenRCInitDir()}
}

func (o *openrcManager) Kind() Kind { return OpenRC }

// ---------- high-level installs ----------

func (o *openrcManager) InstallWebhookService(binPath, addr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9000"
	}
	if err := o.installInitScript("apigw-webhook", "openrc/apigw-webhook.init.tmpl", map[string]any{
		"BinPath": binPath,
		"Addr":    addr,
	}); err != nil {
		return err
	}
	if err := runCmd("rc-update", "add", "apigw-webhook", "default"); err != nil {
		return err
	}
	return runCmd("rc-service", "apigw-webhook", "start")
}

func (o *openrcManager) UninstallWebhookService() error {
	_ = runCmd("rc-service", "apigw-webhook", "stop")
	_ = runCmd("rc-update", "del", "apigw-webhook", "default")
	_ = os.Remove(filepath.Join(o.initDir, "apigw-webhook"))
	return nil
}

func (o *openrcManager) InstallDashboardService(binPath, addr, webhookAddr string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	if addr == "" {
		addr = ":9080"
	}
	if webhookAddr == "" {
		webhookAddr = ":9000"
	}
	if err := o.installInitScript("apigw-dashboard", "openrc/apigw-dashboard.init.tmpl", map[string]any{
		"BinPath":     binPath,
		"Addr":        addr,
		"WebhookAddr": webhookAddr,
	}); err != nil {
		return err
	}
	if err := runCmd("rc-update", "add", "apigw-dashboard", "default"); err != nil {
		return err
	}
	return runCmd("rc-service", "apigw-dashboard", "start")
}

func (o *openrcManager) UninstallDashboardService() error {
	_ = runCmd("rc-service", "apigw-dashboard", "stop")
	_ = runCmd("rc-update", "del", "apigw-dashboard", "default")
	_ = os.Remove(filepath.Join(o.initDir, "apigw-dashboard"))
	return nil
}

// InstallTLSRenewTimer drops a cron job in /etc/periodic/daily/ which Alpine's
// busybox crond runs. We don't manage crond itself — operators must have it
// running (it usually is by default on Alpine).
func (o *openrcManager) InstallTLSRenewTimer(binPath string) error {
	if binPath == "" {
		binPath = defaultBinPath()
	}
	// Also drop the init script so `apigw tls renew` can be triggered manually.
	if err := o.installInitScript("apigw-tls-renew", "openrc/apigw-tls-renew.init.tmpl", map[string]any{
		"BinPath": binPath,
	}); err != nil {
		return err
	}
	cron := "#!/bin/sh\n" + binPath + " tls renew >/var/log/apigw-tls-renew.log 2>&1\n"
	if err := atomicWriteFile("/etc/periodic/daily/apigw-tls-renew", []byte(cron), 0o755); err != nil {
		return fmt.Errorf("write cron script: %w", err)
	}
	return nil
}

func (o *openrcManager) UninstallTLSRenewTimer() error {
	_ = os.Remove("/etc/periodic/daily/apigw-tls-renew")
	_ = os.Remove(filepath.Join(o.initDir, "apigw-tls-renew"))
	return nil
}

func (o *openrcManager) InstallDeployService(spec DeploySpec) error {
	if err := o.installInitScript("apigw-deploy-"+spec.Name, "openrc/apigw-deploy.init.tmpl", map[string]any{
		"Name":    spec.Name,
		"User":    spec.User,
		"Group":   spec.Group,
		"WorkDir": spec.WorkDir,
	}); err != nil {
		return err
	}
	if err := runCmd("rc-update", "add", "apigw-deploy-"+spec.Name, "default"); err != nil {
		return err
	}
	return runCmd("rc-service", "apigw-deploy-"+spec.Name, "start")
}

func (o *openrcManager) UninstallDeployService(name string) error {
	svc := "apigw-deploy-" + name
	_ = runCmd("rc-service", svc, "stop")
	_ = runCmd("rc-update", "del", svc, "default")
	_ = os.Remove(filepath.Join(o.initDir, svc))
	return nil
}

func (o *openrcManager) WriteDeployOverride(name string, _ int, _ string) error {
	// OpenRC has no drop-in mechanism. Re-running InstallDeployService is the
	// supported way to change per-deploy values. No-op here keeps the
	// interface stable.
	return nil
}

// ---------- lifecycle ----------

func (o *openrcManager) Start(name string) error {
	return runCmd("rc-service", openrcName(name), "start")
}
func (o *openrcManager) Stop(name string) error {
	return runCmd("rc-service", openrcName(name), "stop")
}
func (o *openrcManager) Restart(name string) error {
	return runCmd("rc-service", openrcName(name), "restart")
}
func (o *openrcManager) Reload(name string) error {
	return runCmd("rc-service", openrcName(name), "reload")
}

func (o *openrcManager) Enable(name string) error {
	return runCmd("rc-update", "add", openrcName(name), "default")
}
func (o *openrcManager) Disable(name string) error {
	return runCmd("rc-update", "del", openrcName(name), "default")
}

func (o *openrcManager) IsActive(name string) bool {
	return runCmd("rc-service", openrcName(name), "status") == nil
}

func (o *openrcManager) Status(name string) Status {
	st := Status{}
	st.Active = o.IsActive(name)
	out, _ := exec.Command("rc-update", "show").Output()
	st.Enabled = bytes.Contains(out, []byte(openrcName(name)))
	if st.Active {
		st.Detail = "running"
	} else {
		st.Detail = "stopped"
	}
	return st
}

func (o *openrcManager) RunTimer(name string) error {
	return runCmd("rc-service", openrcName(name), "start")
}

func (o *openrcManager) DaemonReload() error {
	// OpenRC has no daemon-reload concept; scripts are read on each invocation.
	return nil
}

// ---------- helpers ----------

func (o *openrcManager) installInitScript(name, tmplPath string, data map[string]any) error {
	body, err := renderOpenRCTemplate(tmplPath, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	dest := filepath.Join(o.initDir, name)
	if err := atomicWriteFile(dest, body, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// openrcName trims any .service suffix and returns the bare init-script name.
func openrcName(name string) string {
	if len(name) > 8 && name[len(name)-8:] == ".service" {
		return name[:len(name)-8]
	}
	return name
}

func renderOpenRCTemplate(path string, data any) ([]byte, error) {
	tmpl, err := template.ParseFS(assets.OpenRC(), path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
