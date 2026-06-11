package system

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strconv"
)

// EnsureSystemUser creates a system user with no shell if it does not already
// exist, and ensures its home directory exists + is owned by the user.
// Idempotent.
//
// Used to provision `apigw-run` — the single identity every deploy uses for
// both the build phase and the runtime systemd unit.
//
// Home dir matters: even though apigw-run never logs in interactively,
// `systemd-run --uid=apigw-run` sets $HOME from /etc/passwd before the build
// command runs. Tools like npm/cargo/pip then try to mkdir caches under that
// path; if the dir doesn't exist + isn't writable by the user, the build
// fails with EACCES on the very first dependency install.
//
// On distros where `useradd` is missing (Alpine: `adduser`), the caller gets
// a clear error and can retry with a documented manual command.
func EnsureSystemUser(name string) error {
	u, err := user.Lookup(name)
	if err == nil {
		// User exists — backfill the home dir if it's missing (covers
		// installs created by older apigw versions that used --no-create-home).
		//
		// Best-effort: the home was already created at useradd time on the
		// host. The webhook/UI deploy path re-runs this from inside the
		// hardened apigw-dashboard unit (ProtectHome=true), where /home is
		// read-only/invisible — a failed mkdir there is a sandbox artifact,
		// not a real error, and must not kill an otherwise-fine deploy.
		_ = ensureHome(u)
		return nil
	}
	if _, err := exec.LookPath("useradd"); err != nil {
		return errors.New("useradd not found — create the user manually: `useradd --system --create-home --shell /usr/sbin/nologin " + name + "`")
	}
	if err := Run("useradd",
		"--system",
		"--create-home",
		"--shell", "/usr/sbin/nologin",
		"--user-group",
		name,
	); err != nil {
		return err
	}
	u, err = user.Lookup(name)
	if err != nil {
		return err
	}
	return ensureHome(u)
}

func ensureHome(u *user.User) error {
	if u.HomeDir == "" {
		return nil
	}
	if err := os.MkdirAll(u.HomeDir, 0o750); err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	return os.Chown(u.HomeDir, uid, gid)
}
