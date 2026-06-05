package deploy

import "path/filepath"

// BaseDir is the root of apigw's deploy state. Each deploy gets a subdir
// containing:
//
//	releases/<sha>/        — the git checkout, build artifacts
//	current -> releases/X  — symlink flipped atomically after health-check
//	logs/                  — files the app writes itself (we encourage stdout)
//	app.env                — secrets file, owned root:apigw-run mode 0600
const BaseDir = "/var/lib/apigw"

// DeployDir returns /var/lib/apigw/<name>.
func DeployDir(name string) string { return filepath.Join(BaseDir, name) }

// ReleaseDir returns /var/lib/apigw/<name>/releases/<sha>.
func ReleaseDir(name, sha string) string {
	return filepath.Join(BaseDir, name, "releases", sha)
}

// ReleasesDir returns /var/lib/apigw/<name>/releases/.
func ReleasesDir(name string) string { return filepath.Join(BaseDir, name, "releases") }

// CurrentSymlink returns /var/lib/apigw/<name>/current.
func CurrentSymlink(name string) string {
	return filepath.Join(BaseDir, name, "current")
}

// EnvFile returns /etc/apigw/<name>.env (owned by root, group apigw-run, 0600).
// Read by the systemd unit via EnvironmentFile=-.
func EnvFile(name string) string {
	return filepath.Join("/etc/apigw", name+".env")
}

// SSHKeyPath is the deploy SSH private key used for private git repos.
//
// One key, all deploys — operators add it as a Deploy Key on each repo
// rather than juggling per-deploy secrets. Reused-key risk is mitigated by
// "ed25519 + 0600 + apigw-managed only" — same trade Fly/Heroku make.
const SSHKeyPath = "/var/lib/apigw/.ssh/deploy_ed25519"

// SSHPubKeyPath is the corresponding public key, what users paste into GitHub.
const SSHPubKeyPath = "/var/lib/apigw/.ssh/deploy_ed25519.pub"

// SSHKnownHosts is where we record host fingerprints so git clone doesn't
// prompt. We pre-seed github.com on first ssh-key generation.
const SSHKnownHosts = "/var/lib/apigw/.ssh/known_hosts"
