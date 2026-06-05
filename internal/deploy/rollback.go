package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RollbackToPrevious flips the deploy's `current` symlink back to the
// release directory immediately preceding the one it currently points
// at (by mtime). Returns the SHA we rolled back to, or an error if
// there's nothing to roll back to.
//
// Use case: a deploy succeeds health checks but starts erroring under
// real traffic. The on-call operator wants to revert to the last-known
// good without re-running clone+build. This skips the entire build
// stage and just flips the symlink + reloads the unit.
//
// Caller must reload the systemd unit (or whatever supervises the
// deploy) after this returns — RollbackToPrevious only touches the
// symlink.
func RollbackToPrevious(name string) (string, error) {
	releases := ReleasesDir(name)
	entries, err := os.ReadDir(releases)
	if err != nil {
		return "", fmt.Errorf("read releases: %w", err)
	}
	currentTarget, _ := os.Readlink(CurrentSymlink(name))
	currentSHA := filepath.Base(currentTarget)

	type rel struct {
		sha string
		ts  int64
	}
	candidates := make([]rel, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "_staging") {
			continue
		}
		if e.Name() == currentSHA {
			// We're rolling AWAY from this one; skip.
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		candidates = append(candidates, rel{e.Name(), info.ModTime().Unix()})
	}
	if len(candidates) == 0 {
		return "", errors.New("rollback: no prior release on disk")
	}
	// Sort newest-first by mtime (insertion sort; the slice is tiny —
	// typically ≤ 5 entries because PruneOldReleases keeps that many).
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j-1].ts < candidates[j].ts; j-- {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
		}
	}
	target := candidates[0].sha
	prevDir := filepath.Join(releases, target)
	if err := flipSymlink(CurrentSymlink(name), prevDir); err != nil {
		return "", fmt.Errorf("flip symlink: %w", err)
	}
	return target, nil
}
