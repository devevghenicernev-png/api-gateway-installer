package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/system"
	"github.com/devevghenicernev-png/apigw/internal/system/svcmgr"
)

// RunUser is the dedicated system user every apigw deployment runs under.
//
// One user (not per-deploy) keeps StateDirectory ownership stable across
// `apigw deploy add` calls. UID churn from DynamicUser=yes thrashes journald
// log ownership.
const (
	RunUser  = "apigw-run"
	RunGroup = "apigw-run"
)

// UnitDir is kept for callers that surface it in messages or doctor output.
const UnitDir = "/etc/systemd/system"

// activeSpec is the per-deploy DeploySpec the next InstallTemplateUnit will
// register with the supervisor. Pre-Phase-3.1 layout: the template + override
// pattern is systemd-specific, so on macOS/OpenRC we register a per-deploy
// service directly with whatever fields we have.
//
// Callers go through InstallTemplateUnit + WriteOverride for historical
// reasons; we accumulate state and let the svcmgr impl decide how to render.
var activeSpec = map[string]svcmgr.DeploySpec{}

// InstallTemplateUnit ensures the deploy template + per-host prerequisites
// are in place. Idempotent — repeated calls are safe.
//
// On systemd: writes /etc/systemd/system/apigw-deploy@.service.
// On launchd/OpenRC: deferred until WriteOverride supplies the name; the
// concrete per-deploy unit is installed there.
func InstallTemplateUnit() error {
	if err := system.EnsureSystemUser(RunUser); err != nil {
		return fmt.Errorf("ensure user: %w", err)
	}
	if svcmgr.Active().Kind() == svcmgr.Systemd {
		// Pre-create the systemd template so subsequent WriteOverride drops
		// just need the per-name dir.
		return svcmgr.Active().InstallDeployService(svcmgr.DeploySpec{
			Name:  "_template", // unused; impl writes the template, not an instance
			User:  RunUser,
			Group: RunGroup,
		})
	}
	// Non-systemd impls install on-demand from WriteOverride / Enable.
	return nil
}

// WriteOverride writes the per-deploy unit so the supervisor knows how to
// run this specific deployment.
//
// On systemd this is a drop-in override.conf in apigw-deploy@<name>.service.d/.
// On launchd/OpenRC this installs the per-deploy plist/init script with
// the current values.
func WriteOverride(name string) error {
	spec := svcmgr.DeploySpec{
		Name:    name,
		User:    RunUser,
		Group:   RunGroup,
		WorkDir: filepath.Join(BaseDir(), name, "current"),
		EnvFile: EnvFile(name),
	}
	activeSpec[name] = spec
	if svcmgr.Active().Kind() == svcmgr.Systemd {
		// Drop-in form: per-deploy override conf in addition to the template.
		return svcmgr.Active().WriteDeployOverride(name, 0, EnvFile(name))
	}
	// Install the concrete per-deploy unit now (no template/instance split).
	return svcmgr.Active().InstallDeployService(spec)
}

// EnsureEnvFile creates <ConfigDir>/<name>.env if missing. Owned by root,
// group apigw-run, mode 0640 so the running deploy can read but not write.
//
// On systems where the apigw-run group doesn't exist yet (first install),
// we still create the file 0600 root:root — InstallTemplateUnit creates the
// user and group, after which a subsequent call will fix ownership.
func EnsureEnvFile(name string) error {
	path := EnvFile(name)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicWriteFile(path, []byte("# apigw deploy "+name+" — environment variables\n"), 0o600); err != nil {
		return err
	}
	// Best-effort chgrp; fail silently when the group doesn't exist yet.
	_ = system.Run("chgrp", RunGroup, path)
	_ = os.Chmod(path, 0o640)
	return nil
}

// Enable starts the unit and configures it to start on boot.
func Enable(name string) error { return svcmgr.Active().Enable(supervisorName(name)) }

// Restart triggers a restart of the deploy unit. Used by `deploy run` when
// the symlink swap is already in place.
func Restart(name string) error { return svcmgr.Active().Restart(supervisorName(name)) }

// Stop drains the unit.
func Stop(name string) error { return svcmgr.Active().Stop(supervisorName(name)) }

// Disable removes the unit from boot startup and uninstalls the drop-in.
func Disable(name string) error {
	if err := svcmgr.Active().UninstallDeployService(name); err != nil {
		return err
	}
	return svcmgr.Active().DaemonReload()
}

// IsActive reports whether the supervisor considers the unit running.
func IsActive(name string) bool { return svcmgr.Active().IsActive(supervisorName(name)) }

// JournalCmd returns the args for `journalctl -u <unit>`. Returns an empty
// slice on non-systemd platforms where journald isn't available; callers
// should fall back to a different log source.
func JournalCmd(name string) []string {
	if svcmgr.Active().Kind() != svcmgr.Systemd {
		return nil
	}
	return []string{"-u", "apigw-deploy@" + name + ".service"}
}

// supervisorName converts a deploy name into the logical name svcmgr knows.
// Systemd: apigw-deploy@<name>.service ; launchd/OpenRC: apigw-deploy-<name>.
func supervisorName(name string) string {
	if svcmgr.Active().Kind() == svcmgr.Systemd {
		return "apigw-deploy@" + name + ".service"
	}
	return "apigw-deploy-" + name
}

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

// SanitizeName returns a systemd-safe instance name. systemd accepts most
// characters but rejects '/' and ':'; we also lowercase for consistency.
// Launchd and OpenRC don't share the restriction but use the same rule for
// path/label sanity.
func SanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return s
}

// Silence unused-import on platforms where paths is only referenced indirectly.
var _ = paths.ConfigDir
