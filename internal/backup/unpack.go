package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UnpackOptions tunes Unpack().
type UnpackOptions struct {
	// Force overwrites existing files. Without it, Unpack refuses to touch
	// a path that already exists with different content.
	Force bool

	// DryRun reads + validates the archive but writes nothing.
	DryRun bool

	// IncludeQueue, when false, skips restoring jobs.db even if present.
	IncludeQueue bool
}

// ReadManifest reads the manifest entry from `src` without extracting the
// rest. Used by `apigw restore --dry-run` to preview, and by callers that
// want to gate behaviour on schema compatibility.
func ReadManifest(src string) (Manifest, error) {
	f, err := os.Open(src)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		if hdr.Name == "manifest.json" {
			body, err := io.ReadAll(tr)
			if err != nil {
				return Manifest{}, err
			}
			var mf Manifest
			if err := json.Unmarshal(body, &mf); err != nil {
				return Manifest{}, fmt.Errorf("parse manifest: %w", err)
			}
			return mf, nil
		}
	}
	return Manifest{}, errors.New("manifest.json missing from archive")
}

// Unpack extracts `src` into the canonical roots. Verifies SHA-256 against
// the manifest as it goes; refuses to continue on mismatch.
//
// Restore policy:
//   - Schema version in archive must be ≤ current binary's SchemaVersion.
//     Older is OK (we know how to migrate); newer is "your binary is too old".
//   - Without Force, refuse to overwrite an existing file whose content differs.
//   - jobs.db is restored only if both the archive and UnpackOptions allow it.
func Unpack(src string, opts UnpackOptions) error {
	mf, err := ReadManifest(src)
	if err != nil {
		return err
	}
	if mf.SchemaVersion > SchemaVersion {
		return fmt.Errorf("archive schema %d is newer than this binary (%d) — upgrade apigw first",
			mf.SchemaVersion, SchemaVersion)
	}
	hashIndex := make(map[string]string, len(mf.Files))
	for _, fs := range mf.Files {
		hashIndex[fs.Path] = fs.SHA256
	}

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == "manifest.json" {
			continue
		}
		// Skip jobs.db unless caller opts in.
		if !opts.IncludeQueue && strings.HasSuffix(hdr.Name, "jobs.db") {
			continue
		}

		// Path-traversal guard. The tar must contain ONLY entries under our
		// canonical roots. Anything that escapes (../) or names a path
		// outside the allowlist gets the archive rejected outright — silently
		// skipping risks confused-deputy attacks where the operator runs
		// restore as root and writes /root/.bashrc.
		target, err := safeTarget(hdr.Name)
		if err != nil {
			return fmt.Errorf("rejected archive entry %q: %w", hdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeDir {
			if !opts.DryRun {
				mode := os.FileMode(hdr.Mode).Perm()
				if mode == 0 {
					mode = 0o755
				}
				if err := os.MkdirAll(target, mode); err != nil {
					return fmt.Errorf("mkdir %s: %w", target, err)
				}
			}
			continue
		}
		// Symlinks (e.g. /var/lib/apigw/<deploy>/current → releases/<sha>):
		// Pack records them so restore reinstates the live release pointer.
		// We don't run Linkname through safeTarget — the link's *contents*
		// can legitimately point anywhere; only the entry's *path* matters
		// for traversal safety, and that was checked above.
		if hdr.Typeflag == tar.TypeSymlink {
			if opts.DryRun {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s → %s: %w", target, hdr.Linkname, err)
			}
			continue
		}
		// Read body.
		body, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read %s: %w", hdr.Name, err)
		}

		// Hash verify.
		if want, ok := hashIndex[hdr.Name]; ok {
			sum := sha256.Sum256(body)
			got := hex.EncodeToString(sum[:])
			if got != want {
				return fmt.Errorf("checksum mismatch for %s (manifest=%s, file=%s)",
					hdr.Name, want, got)
			}
		}

		if opts.DryRun {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
		}

		// Overwrite gating.
		if !opts.Force {
			if existing, rerr := os.ReadFile(target); rerr == nil {
				existingSum := sha256.Sum256(existing)
				newSum := sha256.Sum256(body)
				if existingSum != newSum {
					return fmt.Errorf("%s already exists with different content (use --force to overwrite)", target)
				}
			}
		}

		// Atomic write: temp + rename. We fsync the body before rename so a
		// power loss between rename and disk-flush can't leave a torn file
		// at the target path. Mode is taken straight from the archive (no
		// umask clobber).
		tmp := target + ".restore-tmp"
		mode := os.FileMode(hdr.Mode).Perm()
		if mode == 0 {
			mode = 0o600
		}
		if err := writeFsync(tmp, body, mode); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("rename %s → %s: %w", tmp, target, err)
		}
	}
	return nil
}

// safeTarget validates a tar entry name and returns the absolute path it
// must extract to.
//
// Rules (fail-closed):
//   - reject absolute paths
//   - reject any "." or ".." component (even legitimate-looking ones like
//     "foo/../bar"; the validation is on the archive's intent, not on the
//     post-cleanup path)
//   - reject null bytes (Linux open(2) rejects them but tarballs don't)
//   - after cleaning, the path must start with one of our canonical roots
//     so a fully-encoded "var/../etc/shadow" can't escape into /etc/shadow
//     unless /etc happens to be in the allowlist (it is, but only under
//     /etc/apigw).
func safeTarget(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty name")
	}
	if filepath.IsAbs(name) {
		return "", errors.New("absolute path not allowed")
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("null byte in name")
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, p := range parts {
		if p == "." || p == ".." {
			return "", fmt.Errorf("traversal component %q", p)
		}
	}
	clean := filepath.Clean("/" + name)
	allowed := false
	for _, root := range allowedRoots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("path %s outside allowed roots", clean)
	}
	return clean, nil
}

// allowedRoots is the union of canonical roots packed by Pack() — anything
// outside this list comes from a malicious or corrupt archive.
var allowedRoots = []string{
	"/etc/apigw",
	"/var/lib/apigw",
}

// writeFsync writes body to path, fsyncs, and chmods. Caller normally
// renames over the live file afterwards.
func writeFsync(path string, body []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
