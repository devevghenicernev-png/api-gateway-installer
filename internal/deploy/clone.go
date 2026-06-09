package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// CloneRequest captures everything Clone() needs.
type CloneRequest struct {
	Name   string // deploy name, used to namespace the release dir
	Repo   string // https://... or git@github.com:... or ssh://...
	Branch string // "" → repo default
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
		// Already have this commit; the staged copy is redundant.
		return CloneResult{SHA: sha, Path: finalPath}, nil
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
	cleanupStaging = false
	return CloneResult{SHA: sha, Path: finalPath}, nil
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
		return auth, nil
	}
	// HTTPS with embedded credentials (https://user:token@…) is honoured by
	// go-git automatically — no extra wiring needed.
	return nil, nil
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
