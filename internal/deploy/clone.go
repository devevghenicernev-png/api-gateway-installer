package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/devevghenicernev-png/apigw/internal/system"
)

// CloneRequest captures everything Clone() needs.
type CloneRequest struct {
	Name   string // deploy name, used to namespace the release dir
	Repo   string // https://... or git@github.com:... or ssh://...
	Branch string // "" → repo default
	// Force, when true, removes any pre-existing release dir for the
	// resolved SHA before staging the new clone. Without this, --force
	// at the Apply layer still hits Clone's idempotent short-circuit
	// (and a stale build-artifact tree from the previous attempt may
	// be owned by the wrong user, breaking the re-build).
	Force bool
}

// CloneResult is what Clone returns.
type CloneResult struct {
	SHA  string // resolved HEAD commit
	Path string // /var/lib/apigw/<name>/releases/<sha>
}

// Clone fetches `req.Repo` at `req.Branch` into a new release directory.
//
// Sequence:
//  1. Clone into /var/lib/apigw/<name>/releases/_staging/<random>/ (Depth=1).
//  2. Read HEAD commit SHA.
//  3. Rename _staging/<random>/ → releases/<sha>/.
//  4. Return CloneResult.
//
// If a release at releases/<sha>/ already exists and is intact, we delete
// the staged copy and return that path (idempotent — no double build).
//
// SSH transport is wired automatically when the URL starts with `git@` or
// `ssh://`; we use the deploy key at SSHKeyPath and require it to exist.
func Clone(ctx context.Context, req CloneRequest) (CloneResult, error) {
	if req.Name == "" || req.Repo == "" {
		return CloneResult{}, errors.New("clone: name and repo are required")
	}
	releases := ReleasesDir(req.Name)
	if err := os.MkdirAll(releases, 0o755); err != nil {
		return CloneResult{}, fmt.Errorf("mkdir %s: %w", releases, err)
	}
	// Best-effort GC of older _staging dirs left by previous crashed
	// clones. defer-based cleanup misses SIGKILL/OOM; this hook
	// catches them on the next Clone for the same deploy.
	if oldEntries, _ := os.ReadDir(releases); oldEntries != nil {
		for _, e := range oldEntries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "_staging-") {
				_ = os.RemoveAll(filepath.Join(releases, e.Name()))
			}
		}
	}
	staging, err := os.MkdirTemp(releases, "_staging-")
	if err != nil {
		return CloneResult{}, fmt.Errorf("staging dir: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	auth, err := authFor(req.Repo)
	if err != nil {
		return CloneResult{}, err
	}

	opts := &git.CloneOptions{
		URL:               req.Repo,
		Depth:             1,
		SingleBranch:      true,
		Tags:              git.NoTags,
		RecurseSubmodules: git.NoRecurseSubmodules,
		Auth:              auth,
	}
	if req.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(req.Branch)
	}
	repo, err := git.PlainCloneContext(ctx, staging, false, opts)
	if err != nil {
		return CloneResult{}, fmt.Errorf("git clone: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return CloneResult{}, fmt.Errorf("git head: %w", err)
	}
	sha := head.Hash().String()
	finalPath := ReleaseDir(req.Name, sha)

	if _, err := os.Stat(finalPath); err == nil {
		if req.Force {
			if err := forceRemoveAll(finalPath); err != nil {
				return CloneResult{}, fmt.Errorf("force-remove existing release %s: %w", finalPath, err)
			}
		} else {
			// Already have this commit; the staged copy is redundant.
			return CloneResult{SHA: sha, Path: finalPath}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return CloneResult{}, fmt.Errorf("stat %s: %w", finalPath, err)
	}

	if err := os.Rename(staging, finalPath); err != nil {
		// A concurrent Clone for the same SHA may have already renamed; if so
		// treat as success (idempotency, same as the Stat-found branch above).
		// Any other error propagates.
		if _, sterr := os.Stat(finalPath); sterr == nil {
			return CloneResult{SHA: sha, Path: finalPath}, nil
		}
		return CloneResult{}, fmt.Errorf("rename %s → %s: %w", staging, finalPath, err)
	}
	// MkdirTemp defaults to 0700 — without this a non-root caller can't even
	// cd into the release dir, and `npm install`/custom build commands fail
	// with "can't cd".
	_ = os.Chmod(finalPath, 0o755)
	// Chown the tree to the single deploy user once, here. Both the build
	// (systemd-run --uid=apigw-run) and the runtime systemd unit run as this
	// user, so nothing ever hands ownership back — no chown dance, no EACCES
	// on rebuild. Without it, npm install / the running app would hit EACCES
	// writing node_modules, uploads/, SQLite files, etc.
	_ = chownTree(finalPath, system.DeployUser)
	cleanupStaging = false
	return CloneResult{SHA: sha, Path: finalPath}, nil
}

// forceRemoveAll is os.RemoveAll plus a pre-pass that makes every directory
// in the tree writable+executable+readable by its owner. plain
// os.RemoveAll fails on release dirs where npm / cargo / pip dropped a
// 0555 directory (read-only by design) — the entries inside are
// reachable but `unlinkat` can't remove them because we lack write on the
// parent. shell `rm -rf` does the same chmod under the hood; we emulate
// it explicitly to keep dependencies pure-Go.
func forceRemoveAll(root string) error {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort; RemoveAll's pass will report any real failure
		}
		if info.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

// chownTree recursively chowns `root` to the given system user (and their
// primary group). Best-effort — silent on lookup failure so dev / CI hosts
// without the apigw-run user keep working (the build there runs as root).
func chownTree(root, username string) error {
	u, err := user.Lookup(username)
	if err != nil {
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
	return filepath.Walk(root, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Lchown(path, uid, gid)
	})
}

// authFor returns the transport.AuthMethod for the URL, or nil for anonymous
// HTTPS. Public HTTPS clones (the common case) need no auth — go-git handles
// that path with a nil AuthMethod.
func authFor(url string) (transport.AuthMethod, error) {
	if isSSHURL(url) {
		key, err := loadDeployKey()
		if err != nil {
			return nil, fmt.Errorf("ssh auth: %w (run `apigw deploy ssh-key`)", err)
		}
		auth, err := gitssh.NewPublicKeys("git", key, "")
		if err != nil {
			return nil, fmt.Errorf("ssh public keys: %w", err)
		}
		// Pin host-key verification to the apigw-managed known_hosts file
		// rather than relying on go-git's default lookup (which walks
		// $HOME/.ssh/known_hosts). The dashboard/webhook worker runs from
		// a systemd unit with no HOME set, so the default lookup fails
		// outright with "unable to find any valid known_hosts file"; the
		// CLI happened to inherit a HOME and was fine. Forcing the path
		// here makes both paths behave the same.
		if err := ensureKnownHost(url); err != nil {
			return nil, fmt.Errorf("seed known_hosts: %w", err)
		}
		cb, kherr := knownhosts.New(SSHKnownHosts())
		if kherr != nil {
			return nil, fmt.Errorf("known_hosts %s: %w", SSHKnownHosts(), kherr)
		}
		auth.HostKeyCallback = cb
		return auth, nil
	}
	// HTTPS with embedded credentials (https://user:token@…) is honoured by
	// go-git automatically — no extra wiring needed.
	return nil, nil
}

// ensureKnownHost makes sure SSHKnownHosts() contains an entry for the host
// referenced by `url`. On fresh installs the file doesn't exist at all (no
// upstream tool ever seeded it); the lazy alternative — "tell the operator
// to run ssh-keyscan manually" — is a footgun the deploy flow shouldn't
// require. So we shell out to ssh-keyscan ourselves on the first clone per
// host and append the result. Idempotent: re-runs are no-ops once the host
// has any line.
//
// Uses -t rsa,ecdsa,ed25519 so go-git's negotiation finds a match regardless
// of which type the server offers first (GitHub rotates; pinning a single
// type causes "key mismatch" failures).
func ensureKnownHost(repoURL string) error {
	host := sshHostOf(repoURL)
	if host == "" {
		return nil
	}
	khPath := SSHKnownHosts()
	if err := os.MkdirAll(filepath.Dir(khPath), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(khPath), err)
	}
	// Already present? cheap check via grep-like scan.
	if data, err := os.ReadFile(khPath); err == nil {
		// hashed entry (starts with |1|) won't match a plain prefix
		// check, but if anything at all is in the file we assume an
		// operator/installer seeded it intentionally and don't pile on.
		if len(data) > 0 {
			for _, line := range splitLines(string(data)) {
				if line == "" || line[0] == '#' {
					continue
				}
				if strings.HasPrefix(line, host+" ") || strings.HasPrefix(line, "|1|") {
					return nil
				}
			}
		}
	}
	if _, err := exec.LookPath("ssh-keyscan"); err != nil {
		return fmt.Errorf("ssh-keyscan not on PATH (install openssh-client) — cannot seed %s for %s", khPath, host)
	}
	cmd := exec.Command("ssh-keyscan", "-T", "10", "-t", "rsa,ecdsa,ed25519", host)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ssh-keyscan %s: %w", host, err)
	}
	if len(out) == 0 {
		return fmt.Errorf("ssh-keyscan %s returned no keys", host)
	}
	f, err := os.OpenFile(khPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(out)
	return err
}

// sshHostOf extracts the hostname from an SSH URL. Handles both forms:
//   - git@github.com:owner/repo.git → "github.com"
//   - ssh://git@github.com:22/owner/repo → "github.com"
func sshHostOf(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '/' {
			return s[:i]
		}
	}
	return s
}

func splitLines(s string) []string {
	out := []string{}
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	if last < len(s) {
		out = append(out, s[last:])
	}
	return out
}

func isSSHURL(url string) bool {
	return strings.HasPrefix(url, "git@") ||
		strings.HasPrefix(url, "ssh://") ||
		strings.Contains(url, "@") && !strings.HasPrefix(url, "http")
}

func loadDeployKey() ([]byte, error) {
	b, err := os.ReadFile(SSHKeyPath())
	if err != nil {
		return nil, err
	}
	return b, nil
}

// FetchAndCheckout updates an existing release dir to point at a new branch
// tip without re-cloning. Used by `apigw deploy run` when --no-clone is set.
// Currently unused — kept for future fastpath optimisations.
func FetchAndCheckout(ctx context.Context, dir, branch string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", err
	}
	if err := repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))},
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  true,
	}); err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// EnsureReleaseDir verifies a release dir contains an actual repo snapshot.
// Returns an error if the dir is empty or missing — useful for `deploy status`
// to detect a half-rolled-back state.
func EnsureReleaseDir(name, sha string) error {
	if sha == "" {
		return errors.New("ensure release: empty sha")
	}
	dir := ReleaseDir(name, sha)
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s is empty", dir)
	}
	return nil
}

// PruneOldReleases keeps the most recent `keep` directories under
// releases/ and removes the rest. The release pointed at by `current`
// is never pruned.
//
// Safety: if the `current` symlink is broken or missing we REFUSE to
// prune anything — otherwise an empty currentBase would match no
// existing directory and the loop below would happily delete every
// release for that deploy (we hit this when ops manually removed the
// target of the symlink, expecting "best-effort cleanup" to skip it).
// Operators see a clear error instead of silent data loss.
func PruneOldReleases(name string, keep int) error {
	dir := ReleasesDir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	current, _ := os.Readlink(CurrentSymlink(name))
	currentBase := filepath.Base(current)
	if currentBase == "" || currentBase == "." || currentBase == "/" {
		return fmt.Errorf("prune %s: current symlink missing or empty — refusing to prune to avoid wiping every release", name)
	}
	if _, err := os.Stat(filepath.Join(dir, currentBase)); err != nil {
		return fmt.Errorf("prune %s: current symlink points at missing release %q — refusing", name, currentBase)
	}

	type ent struct {
		name string
		mod  int64
	}
	candidates := make([]ent, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == currentBase {
			continue
		}
		if strings.HasPrefix(e.Name(), "_staging") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, ent{e.Name(), info.ModTime().Unix()})
	}
	// Sort newest first.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j-1].mod < candidates[j].mod; j-- {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
		}
	}
	for i, c := range candidates {
		if i < keep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, c.name))
	}
	return nil
}
