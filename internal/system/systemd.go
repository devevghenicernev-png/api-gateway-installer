// Package system houses OS-level helpers that aren't tied to one feature.
//
// Service-supervisor work (install/start/stop systemd units, launchd plists,
// OpenRC scripts) lives behind internal/system/svcmgr. The functions here
// remain as a thin compatibility surface so callers don't all need to import
// svcmgr directly — and so the cross-platform dispatch is in one place.
package system

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/system/svcmgr"
)

// UnitDir is kept for callers that reference "the unit directory" in
// messages or doctor checks. On non-systemd platforms this points at the
// systemd location anyway — the path is informational only.
const UnitDir = "/etc/systemd/system"

// InstallTLSRenewTimer dispatches to the active service supervisor.
func InstallTLSRenewTimer(binPath string) error {
	return svcmgr.Active().InstallTLSRenewTimer(binPath)
}

// UninstallTLSRenewTimer removes the renewal unit/cron job.
func UninstallTLSRenewTimer() error {
	return svcmgr.Active().UninstallTLSRenewTimer()
}

// InstallWebhookUnit installs the webhook daemon under the active supervisor.
func InstallWebhookUnit(binPath, addr string) error {
	return svcmgr.Active().InstallWebhookService(binPath, addr)
}

// UninstallWebhookUnit stops and removes the webhook unit.
func UninstallWebhookUnit() error {
	return svcmgr.Active().UninstallWebhookService()
}

// InstallDashboardUnit installs the consolidated dashboard daemon under
// the active supervisor.
func InstallDashboardUnit(binPath, addr, webhookAddr string) error {
	return svcmgr.Active().InstallDashboardService(binPath, addr, webhookAddr)
}

// UninstallDashboardUnit stops and removes the dashboard unit.
func UninstallDashboardUnit() error {
	return svcmgr.Active().UninstallDashboardService()
}

// Run executes a command with the systemctl/journalctl/etc. convention:
// inherit stderr, capture+return on non-zero. Used by callers that don't need
// stdout — for that, use exec.Command directly.
func Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w (%s)", name, args, err, stderr.String())
	}
	return nil
}

// ResolveSelfBinary returns the absolute path of the running apigw binary,
// falling back to /usr/local/bin/apigw if the call fails. Used to fill in
// {{ .BinPath }} in unit-file templates.
func ResolveSelfBinary() string {
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

// SupervisorKind returns a string naming the active supervisor — useful for
// doctor output and operator-facing error messages.
func SupervisorKind() string { return string(svcmgr.Active().Kind()) }

// init keeps the paths import alive — UnitDir constant predates the paths
// abstraction but downstream callers might still rely on it. Avoid the
// import-cycle risk by referencing one symbol from paths.
var _ = paths.LinuxSystemdUnits
