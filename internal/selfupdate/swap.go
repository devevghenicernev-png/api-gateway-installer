package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// AssetPattern returns the goreleaser-style archive name we expect for
// the current process's OS/arch. Mirrors the `name_template` in
// /.goreleaser.yaml.
//
//	apigw_0.7.0_linux_amd64.tar.gz
//	apigw_0.7.0_linux_arm64.tar.gz
//	apigw_0.7.0_linux_armv7.tar.gz
//	apigw_0.7.0_darwin_arm64.tar.gz
//
// The version arg may include or omit the leading "v" — we strip it.
func AssetPattern(version string) string {
	arch := runtime.GOARCH
	if arch == "arm" {
		// goreleaser appends the GOARM major when building arm.
		arch = "armv7"
	}
	return fmt.Sprintf("apigw_%s_%s_%s.tar.gz", NormaliseVersion(version), runtime.GOOS, arch)
}

// FindAsset picks the asset matching the current platform out of a Release.
// Returns the asset + a "checksums.txt" companion (always named exactly
// "checksums.txt" by goreleaser) used for verification.
func FindAsset(rel Release) (asset Asset, checksums Asset, ok bool) {
	want := AssetPattern(rel.TagName)
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			asset = a
		case "checksums.txt":
			checksums = a
		}
	}
	ok = asset.DownloadURL != "" && checksums.DownloadURL != ""
	return
}

// VerifyMode tells callers how strongly the new binary was authenticated.
// Used by `apigw upgrade` to surface "✓ cosign-verified" vs "! sha256 only".
type VerifyMode string

const (
	VerifyCosign VerifyMode = "cosign+sha256"
	VerifySHA   VerifyMode  = "sha256"
)

// SwapOptions controls verification strictness.
type SwapOptions struct {
	// AllowMissingCosign permits a sha256-only path when cosign isn't on
	// PATH. Default false — we refuse to swap without keyless verification
	// because that's the only protection against a compromised mirror.
	AllowMissingCosign bool
}

// DownloadAndSwap downloads `asset`, verifies it against `checksums`,
// runs cosign-cli for keyless signature verification (mandatory unless
// the caller explicitly opts out), extracts the apigw binary, and
// atomically renames it over the running executable.
//
// Sequence is failure-bias:
//  1. Resolve current /proc/self/exe via os.Executable() + EvalSymlinks.
//  2. Download tarball + checksums.txt + .sig + .pem to a tmp dir.
//  3. cosign verify-blob — REQUIRED. Missing cosign or failed download
//     aborts unless opts.AllowMissingCosign is true.
//  4. sha256 of tarball must appear in checksums.txt under our asset's name.
//  5. Extract the single "apigw" file from the tarball to <tmp>/apigw.
//  6. chmod 0755, rename(2) over the live binary.
//
// Returns the VerifyMode actually used so the CLI can surface it.
func DownloadAndSwap(ctx context.Context, asset, checksums Asset, repo string, opts SwapOptions) (VerifyMode, error) {
	exe, err := selfExe()
	if err != nil {
		return "", fmt.Errorf("locate self: %w", err)
	}

	tmp, err := os.MkdirTemp("", "apigw-upgrade-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	tarPath := filepath.Join(tmp, asset.Name)
	sumPath := filepath.Join(tmp, "checksums.txt")
	if err := download(ctx, asset.DownloadURL, tarPath); err != nil {
		return "", fmt.Errorf("download tarball: %w", err)
	}
	if err := download(ctx, checksums.DownloadURL, sumPath); err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}

	// Cosign keyless verification — mandatory by default. The only way to
	// fall through to sha256-only is the explicit opt-out flag, because
	// without cosign there's no defence against a malicious mirror that
	// happens to publish a matching checksums.txt.
	mode := VerifyCosign
	cosignPath, cosignErr := exec.LookPath("cosign")
	if cosignErr != nil {
		if !opts.AllowMissingCosign {
			return "", fmt.Errorf("cosign not on PATH — install cosign (https://docs.sigstore.dev/cosign/installation/) or rerun with --insecure-skip-cosign")
		}
		mode = VerifySHA
	}
	if repo == "" {
		if !opts.AllowMissingCosign {
			return "", fmt.Errorf("cosign verification requires a known repo")
		}
		mode = VerifySHA
	}
	if mode == VerifyCosign {
		sigURL := siblingURL(checksums.DownloadURL, ".sig")
		pemURL := siblingURL(checksums.DownloadURL, ".pem")
		sigPath := filepath.Join(tmp, "checksums.txt.sig")
		pemPath := filepath.Join(tmp, "checksums.txt.pem")
		if err := download(ctx, sigURL, sigPath); err != nil {
			return "", fmt.Errorf("download cosign signature: %w", err)
		}
		if err := download(ctx, pemURL, pemPath); err != nil {
			return "", fmt.Errorf("download cosign certificate: %w", err)
		}
		if err := runCosign(ctx, cosignPath, repo, sumPath, sigPath, pemPath); err != nil {
			return "", fmt.Errorf("cosign verify-blob failed: %w", err)
		}
	}

	// SHA-256 check (always, even after cosign succeeded).
	wantHash, err := lookupChecksum(sumPath, asset.Name)
	if err != nil {
		return "", err
	}
	gotHash, err := fileSHA256(tarPath)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(wantHash, gotHash) {
		return "", fmt.Errorf("checksum mismatch: manifest=%s file=%s", wantHash, gotHash)
	}

	// Extract the single `apigw` member.
	binPath, err := extractApigw(tarPath, tmp)
	if err != nil {
		return "", fmt.Errorf("extract: %w", err)
	}
	if err := os.Chmod(binPath, 0o755); err != nil {
		return "", err
	}

	// Atomic swap. rename(2) over an executing binary works on Linux/macOS
	// because the kernel keeps the open inode alive for the running process.
	if err := os.Rename(binPath, exe); err != nil {
		// Fallback for cross-device rename (tmp on tmpfs, /usr/local on rootfs).
		if err := copyOver(binPath, exe); err != nil {
			return "", fmt.Errorf("swap: %w", err)
		}
	}
	return mode, nil
}

// selfExe returns the absolute path of the running binary, following any
// symlink (e.g. /usr/local/bin/apigw → /opt/apigw/bin/apigw).
func selfExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	abs, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil
	}
	return abs, nil
}

// download fetches `url` to `dest`. We use a generous timeout because
// release tarballs on slow Pi connections can take minutes.
func download(ctx context.Context, url, dest string) error {
	c := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s → %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// runCosign shells out to `cosign verify-blob` with the keyless flags from
// ARCHITECTURE.md §2. We require the workflow file path to match the repo
// shape `<repo>/.github/workflows/release.yml`.
func runCosign(ctx context.Context, cosignPath, repo, sumPath, sigPath, pemPath string) error {
	identity := fmt.Sprintf("https://github.com/%s/.github/workflows/release.yml@", repo)
	cmd := exec.CommandContext(ctx, cosignPath,
		"verify-blob",
		"--certificate", pemPath,
		"--signature", sigPath,
		"--certificate-identity-regexp", identity+".*",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		sumPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// lookupChecksum finds the entry for `asset` in a goreleaser
// `checksums.txt` (lines of "<hex> <filename>"). Case-insensitive.
func lookupChecksum(sumPath, asset string) (string, error) {
	b, err := os.ReadFile(sumPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[1] == asset || fields[1] == "*"+asset) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found in checksums.txt", asset)
}

// fileSHA256 returns the lowercase hex digest.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractApigw pulls just the apigw binary out of the tarball into `dir`.
// goreleaser archives include LICENSE/README; we ignore them.
func extractApigw(tarPath, dir string) (string, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if base != "apigw" {
			continue
		}
		dest := filepath.Join(dir, "apigw")
		out, err := os.Create(dest)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return dest, nil
	}
	return "", errors.New("apigw binary not found in tarball")
}

// copyOver overwrites `dst` with the contents of `src`. Used when rename(2)
// fails across filesystems.
func copyOver(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func siblingURL(base, suffix string) string {
	return base + suffix
}
