package selfupdate

import (
	"runtime"
	"strings"
	"testing"
)

// TestCosignAssetName_KnownPlatforms is a regression guard for the
// pinned cosign sha256 table. The build host's GOOS/GOARCH must always
// resolve to a non-empty asset + sha; otherwise `apigw upgrade` on this
// platform falls back to the require-cosign-on-PATH error and the
// bootstrap UX is silently disabled.
//
// We treat "build host has a pin" as the contract. If apigw ever starts
// shipping for a platform that cosign doesn't (e.g. plan9 hypothetically)
// this test forces a deliberate update — either add the pin or carve out
// an explicit skip with a code comment.
func TestCosignAssetName_KnownPlatforms(t *testing.T) {
	asset, sha := CosignAssetName()
	if asset == "" || sha == "" {
		t.Fatalf("no pinned cosign for %s/%s — add it to cosign_bootstrap.go or skip this test explicitly with a reason",
			runtime.GOOS, runtime.GOARCH)
	}
	if !strings.HasPrefix(asset, "cosign-"+runtime.GOOS+"-") {
		t.Errorf("asset name %q doesn't start with cosign-%s- — likely wrong row in the switch",
			asset, runtime.GOOS)
	}
	if len(sha) != 64 {
		t.Errorf("sha %q isn't 64 hex chars — pinned sha256 looks malformed", sha)
	}
}

// TestCanBootstrapCosign tracks CosignAssetName — for any platform where
// we have a pin, CanBootstrapCosign must return true; otherwise false.
// Keeps the two functions in sync if someone refactors one.
func TestCanBootstrapCosign(t *testing.T) {
	asset, _ := CosignAssetName()
	got := CanBootstrapCosign()
	want := asset != ""
	if got != want {
		t.Errorf("CanBootstrapCosign() = %v, want %v (asset=%q)", got, want, asset)
	}
}

// TestPinnedCosignVersion_Format catches accidental sloppy edits like
// dropping the leading "v" (sigstore release URLs require it).
func TestPinnedCosignVersion_Format(t *testing.T) {
	v := PinnedCosignVersion()
	if !strings.HasPrefix(v, "v") {
		t.Errorf("pinned cosign version %q missing leading v — sigstore release URLs won't resolve", v)
	}
	if strings.Count(v, ".") < 2 {
		t.Errorf("pinned cosign version %q doesn't look like semver", v)
	}
}
