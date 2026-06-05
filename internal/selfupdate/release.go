// Package selfupdate implements `apigw upgrade`: query GitHub Releases,
// pick the asset for our OS/arch, verify checksum (+ cosign sig if
// available), and atomically swap the running binary.
//
// Trust model: we ONLY trust the checksum file's signature. The asset
// tarball itself is not signed individually — that's how goreleaser +
// cosign's keyless flow works (see ARCHITECTURE.md §"Build & release").
// So: download tarball, recompute its sha256, look it up in checksums.txt,
// verify checksums.txt against checksums.txt.sig + checksums.txt.pem using
// cosign-cli if the binary is on PATH. Refuse swap on any failure.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultRepo is the slug we query when the caller doesn't override.
// Matches the goreleaser config in /.goreleaser.yaml.
const DefaultRepo = "devevghenicernev-png/apigw"

// Release is the trimmed-down shape of GitHub's release JSON.
//
// We don't pull in google/go-github here for the same reason the webhook
// parser doesn't — one endpoint, one method, no need for the whole client.
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	URL     string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
	Body    string  `json:"body"`
}

// Asset is one downloadable file attached to a Release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// LatestRelease fetches the newest release published for `repo`. Honors
// ctx for cancellation. Network errors and 404s are surfaced directly.
//
// We do NOT follow the "/latest" alias when looking for prereleases — that
// matches what `gh release view --latest` does and keeps `apigw upgrade`
// off pre-release tracks unless the user opts in (Phase 9.1: --prerelease).
func LatestRelease(ctx context.Context, repo string) (Release, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("no published release for %s", repo)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Release{}, fmt.Errorf("github releases: %s — %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode release: %w", err)
	}
	return rel, nil
}

// NormaliseVersion strips a leading "v" so callers can compare against
// build.Version uniformly. "v0.1.0" → "0.1.0"; "0.1.0" → "0.1.0".
func NormaliseVersion(s string) string {
	return strings.TrimPrefix(s, "v")
}

// IsNewer returns true if `available` is a strictly newer semver than
// `current`. We do a literal string compare for non-semver tags ("dev",
// "edge", "head") — the most conservative outcome ("no newer version") is
// returned.
//
// Semver compare is a 30-line job but every additional dep on a Pi build
// matters; this manual version is fine for "x.y.z" with optional pre-release.
func IsNewer(current, available string) bool {
	current = NormaliseVersion(current)
	available = NormaliseVersion(available)
	if current == "" || current == "dev" {
		return true // any tagged release is "newer" than an unstamped dev build
	}
	if current == available {
		return false
	}
	cParts := splitVersion(current)
	aParts := splitVersion(available)
	n := len(cParts)
	if len(aParts) > n {
		n = len(aParts)
	}
	for i := 0; i < n; i++ {
		c, a := atZero(cParts, i), atZero(aParts, i)
		if a > c {
			return true
		}
		if a < c {
			return false
		}
	}
	return false
}

func splitVersion(s string) []int {
	// Strip pre-release / build metadata before splitting.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

func atZero(s []int, i int) int {
	if i >= len(s) {
		return 0
	}
	return s[i]
}
