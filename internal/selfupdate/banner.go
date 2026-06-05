package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/build"
)

// CheckInterval is the minimum time between GitHub API queries. 24 h
// matches gh's cadence and stays well under GitHub's 60/hour unauthed
// rate limit even on a host that runs apigw on every shell prompt.
const CheckInterval = 24 * time.Hour

// CacheFile is where we memoise the last check. ~/.cache because the
// XDG spec puts ephemeral derived data there; surviving a reboot is
// fine but it isn't application state.
func CacheFile() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".cache")
		} else {
			dir = filepath.Join(os.TempDir(), "apigw-cache")
		}
	}
	return filepath.Join(dir, "apigw", "update.json")
}

// cacheEntry persists between invocations. Only `Latest` ever matters to
// callers; `LastChecked` gates the next query.
type cacheEntry struct {
	LastChecked time.Time `json:"last_checked"`
	Latest      string    `json:"latest"`
	URL         string    `json:"url,omitempty"`
}

// BackgroundCheck kicks off a non-blocking check for a newer release.
// Returns a `Check` whose Banner() method the caller invokes AFTER the
// user's command has finished — that way the banner shows up cleanly
// below the real output, never racing it.
//
// Disabled when APIGW_NO_UPDATE_CHECK is set (any value). The check
// itself reads cache + makes (at most) one HTTP round-trip; if the
// cache says we checked < CheckInterval ago we don't even open a
// connection.
type Check struct {
	repo    string
	wait    chan struct{}
	latest  string
	url     string
	err     error
	stopped bool
	once    sync.Once
}

// NewBackgroundCheck starts the check goroutine. Cheap; callers should
// always call .Banner() — it's a no-op if the check was disabled.
func NewBackgroundCheck(repo string) *Check {
	c := &Check{repo: repo, wait: make(chan struct{})}
	if disabled() {
		close(c.wait)
		c.stopped = true
		return c
	}
	go c.run()
	return c
}

func disabled() bool {
	if os.Getenv("APIGW_NO_UPDATE_CHECK") != "" {
		return true
	}
	// Don't pester users running dev / unstamped builds — they're
	// almost certainly building from source.
	if NormaliseVersion(build.Version) == "" || build.Version == "dev" {
		return true
	}
	return false
}

func (c *Check) run() {
	defer close(c.wait)
	entry, ok := loadCache()
	if ok && time.Since(entry.LastChecked) < CheckInterval {
		c.latest = entry.Latest
		c.url = entry.URL
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rel, err := LatestRelease(ctx, c.repo)
	if err != nil {
		c.err = err
		return
	}
	c.latest = rel.TagName
	c.url = rel.URL
	_ = saveCache(cacheEntry{
		LastChecked: time.Now().UTC(),
		Latest:      rel.TagName,
		URL:         rel.URL,
	})
}

// Banner writes a one-line "vX.Y.Z is available" note to w iff a newer
// version was discovered. Returns whether it printed anything — handy for
// tests. Blocks up to 250 ms waiting for the background check, then gives
// up so we don't delay shell prompt return.
func (c *Check) Banner(w io.Writer) bool {
	if c == nil || c.stopped {
		return false
	}
	select {
	case <-c.wait:
	case <-time.After(250 * time.Millisecond):
		return false // check is slow; skip rather than block exit
	}
	if c.err != nil || c.latest == "" {
		return false
	}
	if !IsNewer(build.Version, c.latest) {
		return false
	}
	url := c.url
	if url == "" {
		url = "https://github.com/" + c.repo + "/releases/" + c.latest
	}
	fmt.Fprintf(w, "\n%s %s is available — run \x1b[1mapigw upgrade\x1b[0m (current %s)\n   %s\n",
		"\x1b[33m!\x1b[0m",
		strings.TrimSpace(c.latest),
		strings.TrimSpace(build.Version),
		url,
	)
	return true
}

func loadCache() (cacheEntry, bool) {
	b, err := os.ReadFile(CacheFile())
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return cacheEntry{}, false
	}
	return e, true
}

func saveCache(e cacheEntry) error {
	path := CacheFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Cross-filesystem fallback — cache is best-effort.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Ensure errors.Is is used inline so the import is intentional — net
// readability via the typed err comparison.
var _ = errors.Is
