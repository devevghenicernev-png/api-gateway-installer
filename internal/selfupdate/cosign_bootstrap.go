package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// pinnedCosign* mirrors the same pin in scripts/install.sh. Bumping the
// cosign version means updating BOTH this file AND install.sh together
// — sha256s differ per release and the two paths must agree, otherwise
// install.sh and `apigw upgrade` would bootstrap different cosign
// builds on the same host.
//
// Source of truth: https://github.com/sigstore/cosign/releases.
const pinnedCosignVersion = "v3.0.6"

const (
	cosignSHALinuxAMD64  = "c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74"
	cosignSHALinuxARM64  = "bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8"
	cosignSHALinuxARMv7  = "67bd25d32daff5664caf51208c95defcb2ad7ac1296f394fa677bb8bacee62f5"
	cosignSHADarwinAMD64 = "4c3e7af8372d3ca3296e62fa56f23fcbb5721cc6ac1827900d398f110d7cd280"
	cosignSHADarwinARM64 = "5fadd012ae6381a6a29ff86a7d39aa873878852f1073fc90b15995961ecfb084"
)

// CosignAssetName returns the sigstore asset filename for the current
// GOOS/GOARCH (e.g. "cosign-linux-arm64") plus its embedded sha256.
// Returns "", "" when we don't have a pin for this platform — callers
// fall back to the old "cosign not on PATH" error in that case.
//
// Exported so `apigw upgrade --check` can decide whether to advertise
// "cosign+sha256 (bootstrapped)" vs "sha256-only" upfront.
func CosignAssetName() (asset, sha string) {
	goarch := runtime.GOARCH
	if goarch == "arm" {
		// goreleaser convention: cosign-linux-arm corresponds to GOARCH=arm.
		goarch = "arm"
	}
	switch runtime.GOOS + "_" + goarch {
	case "linux_amd64":
		return "cosign-linux-amd64", cosignSHALinuxAMD64
	case "linux_arm64":
		return "cosign-linux-arm64", cosignSHALinuxARM64
	case "linux_arm":
		return "cosign-linux-arm", cosignSHALinuxARMv7
	case "darwin_amd64":
		return "cosign-darwin-amd64", cosignSHADarwinAMD64
	case "darwin_arm64":
		return "cosign-darwin-arm64", cosignSHADarwinARM64
	}
	return "", ""
}

// CanBootstrapCosign reports whether `apigw upgrade` can self-host a
// cosign binary on this platform. False for unknown OS/arch combos
// (e.g. linux/ppc64le, linux/s390x, freebsd).
func CanBootstrapCosign() bool {
	asset, _ := CosignAssetName()
	return asset != ""
}

// PinnedCosignVersion returns the cosign release the bootstrap downloads.
// Exposed so `apigw upgrade --check` can mention it in the plan.
func PinnedCosignVersion() string { return pinnedCosignVersion }

// bootstrapCosign downloads the pinned cosign binary into destDir,
// verifies its sha256 against the embedded value, and returns the
// path to the chmod-755'd binary. Returns an error if we don't have
// a pin for this platform, the download fails, or the sha mismatches.
//
// Trust root: this binary itself. install.sh and `apigw upgrade` ship
// the same sha256s in lockstep; if the bootstrapped binary's sha
// doesn't match, refuse to proceed (proxy / mirror tampering signal).
func bootstrapCosign(ctx context.Context, destDir string) (string, error) {
	asset, want := CosignAssetName()
	if asset == "" {
		return "", fmt.Errorf("no pinned cosign for %s/%s — install cosign manually or pass --insecure-skip-cosign",
			runtime.GOOS, runtime.GOARCH)
	}
	url := fmt.Sprintf("https://github.com/sigstore/cosign/releases/download/%s/%s",
		pinnedCosignVersion, asset)
	binPath := filepath.Join(destDir, "cosign")
	if err := download(ctx, url, binPath); err != nil {
		return "", fmt.Errorf("download pinned cosign %s: %w", pinnedCosignVersion, err)
	}
	got, err := fileSHA256(binPath)
	if err != nil {
		return "", fmt.Errorf("hash pinned cosign: %w", err)
	}
	if !strings.EqualFold(want, got) {
		return "", fmt.Errorf("pinned cosign sha256 mismatch (asset=%s want=%s got=%s) — refusing to use",
			asset, want, got)
	}
	if err := os.Chmod(binPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod pinned cosign: %w", err)
	}
	return binPath, nil
}
