package svcmgr

import (
	"os"
	"os/exec"
	"runtime"
	"sync"
)

// Detect picks the best supervisor for the running OS in this priority:
//
//  1. systemd — if /run/systemd/system exists (the canonical "systemd is PID 1"
//     marker recommended by systemd's own sd_booted(3)).
//  2. launchd — if GOOS=darwin (launchd is always present on macOS).
//  3. OpenRC — if /etc/init.d exists AND rc-service is on PATH.
//  4. None — no supervisor we can drive; install/start are unsupported.
//
// The result is memoized via Active().
func Detect() Manager {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return newSystemd()
	}
	if runtime.GOOS == "darwin" {
		return newLaunchd()
	}
	if _, err := os.Stat("/etc/init.d"); err == nil {
		if _, err := exec.LookPath("rc-service"); err == nil {
			return newOpenRC()
		}
	}
	return noneManager{}
}

var (
	activeOnce sync.Once
	active     Manager
)

// Active returns the detected supervisor for this process. Cached after the
// first call; tests that need a different supervisor should call SetActive.
func Active() Manager {
	activeOnce.Do(func() { active = Detect() })
	return active
}

// SetActive overrides the cached active manager. Intended for tests.
func SetActive(m Manager) {
	activeOnce.Do(func() {}) // burn the once so subsequent Active() returns m
	active = m
}
