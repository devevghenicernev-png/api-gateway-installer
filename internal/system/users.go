package system

import (
	"errors"
	"os/exec"
	"os/user"
)

// EnsureSystemUser creates a system user with no shell and no home directory
// if it does not already exist. Idempotent.
//
// Used to provision `apigw-run` for deployments (one shared identity across
// all deploys) and — Phase 3.1 — `apigw-build` for build sandboxing.
//
// On distros where `useradd` is missing (Alpine: `adduser`), the caller gets
// a clear error and can retry with a documented manual command.
func EnsureSystemUser(name string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return errors.New("useradd not found — create the user manually: `useradd --system --no-create-home --shell /usr/sbin/nologin " + name + "`")
	}
	return Run("useradd",
		"--system",
		"--no-create-home",
		"--shell", "/usr/sbin/nologin",
		"--user-group",
		name,
	)
}
