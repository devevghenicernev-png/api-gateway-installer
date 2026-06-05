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
	"time"

	"github.com/devevghenicernev-png/apigw/internal/build"
)

// PackOptions tunes Pack().
type PackOptions struct {
	// IncludeQueue packs /var/lib/apigw/jobs.db. Off by default because
	// restoring a queue onto a fresh host can re-trigger old deploys.
	IncludeQueue bool
}

// Pack writes a tar.gz archive of apigw state to `dest`. The manifest is
// the FIRST entry, so a partial archive can still be inspected.
//
// Atomic: writes to <dest>.tmp, fsyncs, renames onto dest. Failures leave
// the tmp file in place so the caller can decide whether to keep it.
//
// Returns the manifest that was actually written.
func Pack(dest string, opts PackOptions) (Manifest, error) {
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Manifest{}, fmt.Errorf("create %s: %w", tmp, err)
	}
	defer func() {
		_ = f.Close()
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	host, _ := os.Hostname()
	mf := Manifest{
		SchemaVersion: SchemaVersion,
		APIGWVersion:  build.Version,
		APIGWCommit:   build.Commit,
		CreatedAt:     time.Now().UTC(),
		Host:          host,
		IncludesQueue: opts.IncludeQueue,
	}

	// Reserve a slot for the manifest header so it sorts first in the tar.
	// We can't write the manifest until we know the file list, so we keep
	// a sentinel here and overwrite after collecting. Implementation:
	// write the placeholder header now (empty payload), record offset.
	// Simpler alternative: write files first, manifest last. We pick that.

	srcs := append([]string{}, roots...)
	if opts.IncludeQueue {
		srcs = append(srcs, optionalRoots...)
	}
	for _, src := range srcs {
		if err := packTree(tw, src, &mf); err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			os.Remove(tmp)
			return mf, fmt.Errorf("pack %s: %w", src, err)
		}
	}

	// Manifest goes LAST so we have the complete file list. Restore reads
	// the whole tar first (cheap; we're talking KB) so order doesn't matter
	// to correctness.
	body, _ := json.MarshalIndent(mf, "", "  ")
	if err := writeFile(tw, "manifest.json", body, 0o644); err != nil {
		tw.Close()
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return mf, fmt.Errorf("write manifest: %w", err)
	}

	if err := tw.Close(); err != nil {
		return mf, err
	}
	if err := gz.Close(); err != nil {
		return mf, err
	}
	if err := f.Sync(); err != nil {
		return mf, err
	}
	if err := f.Close(); err != nil {
		return mf, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return mf, fmt.Errorf("rename %s → %s: %w", tmp, dest, err)
	}
	return mf, nil
}

// packTree walks src and adds every regular file + directory to the tar.
// Symlinks are followed (we don't preserve them).
func packTree(tw *tar.Writer, src string, mf *Manifest) error {
	st, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil // missing roots are OK (e.g. /var/lib/apigw/certs before first cert)
	}
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return packFile(tw, src, mf)
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Directories are recorded so empty dirs survive restore.
			rel := archivePath(path)
			hdr := &tar.Header{
				Name:     rel + "/",
				Mode:     int64(info.Mode().Perm()),
				ModTime:  info.ModTime(),
				Typeflag: tar.TypeDir,
			}
			return tw.WriteHeader(hdr)
		}
		// Symlinks (e.g. /var/lib/apigw/<deploy>/current) are recorded so
		// restore reinstates the live release pointer. Previously we
		// skipped them entirely, which meant `apigw restore` left every
		// service down until next deploy.
		if info.Mode()&os.ModeSymlink != 0 {
			target, lerr := os.Readlink(path)
			if lerr != nil {
				return nil // best-effort; broken symlink not worth aborting on
			}
			rel := archivePath(path)
			return tw.WriteHeader(&tar.Header{
				Name:     rel,
				Linkname: target,
				Mode:     int64(info.Mode().Perm()),
				ModTime:  info.ModTime(),
				Typeflag: tar.TypeSymlink,
			})
		}
		if !info.Mode().IsRegular() {
			return nil // skip sockets, devices
		}
		return packFile(tw, path, mf)
	})
}

func packFile(tw *tar.Writer, path string, mf *Manifest) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])

	rel := archivePath(path)
	hdr := &tar.Header{
		Name:    rel,
		Mode:    int64(info.Mode().Perm()),
		Size:    int64(len(b)),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(b); err != nil {
		return err
	}
	mf.Files = append(mf.Files, FileSpec{
		Path:   rel,
		Origin: path,
		Size:   info.Size(),
		Mode:   uint32(info.Mode().Perm()),
		SHA256: hash,
	})
	return nil
}

// archivePath strips the leading "/" so the tar contains relative entries.
// Restore re-prepends "/" — we never write outside the canonical roots.
func archivePath(p string) string {
	p = strings.TrimPrefix(p, "/")
	return filepath.ToSlash(p)
}

// writeFile is a small helper for inline content (manifest.json).
func writeFile(tw *tar.Writer, name string, content []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(content)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := io.Copy(tw, strings.NewReader(string(content)))
	return err
}
