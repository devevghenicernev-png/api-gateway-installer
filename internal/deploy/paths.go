package deploy

import (
	"path/filepath"

	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// All deploy state lives under paths.StateDir() (overridable via
// APIGW_STATE_DIR). Per-deploy .env files live under paths.ConfigDir()
// (APIGW_CONFIG_DIR). Defaults are /var/lib/apigw and /etc/apigw.
//
// Each deploy gets a subdir containing:
//
//	releases/<sha>/        — the git checkout, build artifacts
//	current -> releases/X  — symlink flipped atomically after health-check
//	logs/                  — files the app writes itself (we encourage stdout)
//	app.env                — secrets file, owned root:apigw-run mode 0600

// BaseDir returns the deploy state root (defaults to /var/lib/apigw).
func BaseDir() string { return paths.StateDir() }

// DeployDir returns <StateDir>/<name>.
func DeployDir(name string) string { return filepath.Join(BaseDir(), name) }

// ReleaseDir returns <StateDir>/<name>/releases/<sha>.
func ReleaseDir(name, sha string) string {
	return filepath.Join(BaseDir(), name, "releases", sha)
}

// ReleasesDir returns <StateDir>/<name>/releases/.
func ReleasesDir(name string) string { return filepath.Join(BaseDir(), name, "releases") }

// CurrentSymlink returns <StateDir>/<name>/current.
func CurrentSymlink(name string) string {
	return filepath.Join(BaseDir(), name, "current")
}

// EnvFile returns <ConfigDir>/<name>.env (owned by root, group apigw-run, 0600).
// Read by the systemd unit via EnvironmentFile=-.
func EnvFile(name string) string {
	return filepath.Join(paths.ConfigDir(), name+".env")
}

// SSHKeyPath is the deploy SSH private key used for private git repos.
//
// One key, all deploys — operators add it as a Deploy Key on each repo
// rather than juggling per-deploy secrets. Reused-key risk is mitigated by
// "ed25519 + 0600 + apigw-managed only" — same trade Fly/Heroku make.
func SSHKeyPath() string {
	return filepath.Join(BaseDir(), ".ssh", "deploy_ed25519")
}

// SSHPubKeyPath is the corresponding public key, what users paste into GitHub.
func SSHPubKeyPath() string {
	return filepath.Join(BaseDir(), ".ssh", "deploy_ed25519.pub")
}

// SSHKnownHosts is where we record host fingerprints so git clone doesn't
// prompt. We pre-seed github.com on first ssh-key generation.
func SSHKnownHosts() string {
	return filepath.Join(BaseDir(), ".ssh", "known_hosts")
}
