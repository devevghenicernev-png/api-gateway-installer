//go:build e2e

// Package e2e_test runs the full apigw stack inside a privileged systemd
// container per supported distro. Slow (~30s/distro): excluded from the
// default `go test ./...` via the `e2e` build tag.
//
//	make e2e                                 # all distros
//	APIGW_E2E_DISTRO=debian12 go test \
//	  -tags=e2e -count=1 -v ./test/e2e/...   # one distro
//
// Test order matters within a single container (state accumulates) — the
// scenarios sub-test calls in install_test.go runs the whole sequence
// against one boot.
package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devevghenicernev-png/apigw/test/e2e/harness"
)

// hostBinary returns the absolute path to the freshly-built apigw binary.
// Override via APIGW_E2E_BINARY for cross-host runs (CI builds on a
// separate matrix entry, e.g. amd64-host runs the arm64 binary inside an
// emulated container).
func hostBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("APIGW_E2E_BINARY"); p != "" {
		return p
	}
	// Default: <repo>/bin/apigw, written by `make build`.
	repo, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// We're in test/e2e/; go up two.
	root := filepath.Join(repo, "..", "..")
	candidate := filepath.Join(root, "bin", "apigw")
	if _, err := os.Stat(candidate); err != nil {
		t.Skipf("apigw binary missing at %s — run `make build` first", candidate)
	}
	abs, _ := filepath.Abs(candidate)
	return abs
}

// pickDistros honors APIGW_E2E_DISTRO (single name) or runs the full
// matrix when unset. Keeps `make e2e` fast on a developer laptop where
// "all three" is overkill.
func pickDistros() []harness.Distro {
	if name := os.Getenv("APIGW_E2E_DISTRO"); name != "" {
		return []harness.Distro{harness.Distro(name)}
	}
	return harness.AllDistros()
}
