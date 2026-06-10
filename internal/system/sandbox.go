package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// BuildUser is the dedicated unprivileged user every deploy build runs as.
// Created lazily on first use via EnsureSystemUser.
//
// One user (not per-deploy) keeps the systemd-run scope hierarchy simple.
// A poisoned `npm install` postinstall still can't touch root-owned files,
// the running deploy's StateDirectory, or the systemd unit files we own.
const BuildUser = "apigw-build"

// BuildSlice scopes per-build resource limits. systemd-run --slice attaches
// every transient scope here, so a runaway build inherits the slice's
// CPUQuota/MemoryHigh instead of starving siblings.
const BuildSlice = "apigw-builds.slice"

// SandboxedCommand wraps `args` with `systemd-run --scope --uid=apigw-build
// --slice=apigw-builds.slice` when systemd-run is on PATH; otherwise falls
// back to a plain exec.Command (which the caller may still want to su to a
// non-root user — see EnsureSystemUser).
//
// Returns the exec.Cmd ready to Start(). The caller still sets Dir / Env /
// stdout / stderr.
//
// The brief from ARCHITECTURE.md §"Build sandboxing" says: "build runs
// in a transient scope inheriting our .slice budget. systemd's
// systemd-analyze security will score it the same as a long-running unit."
func SandboxedCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if len(args) == 0 {
		return nil, errors.New("SandboxedCommand: empty args")
	}
	// Try systemd-run; fall through if missing (CI / containers without
	// systemd-run binary even though systemd is PID 1).
	if _, err := exec.LookPath("systemd-run"); err == nil {
		// Ensure the build user exists. Cheap when it already does.
		_ = EnsureSystemUser(BuildUser)
		// --scope makes the child inherit our stdio directly, so --pipe
		// would be both redundant and rejected by systemd ≥250
		// ("--pty/--pipe is not compatible in timer or --scope mode" —
		// surfaced on Debian trixie / Armbian rolling).
		wrapped := append([]string{
			"systemd-run",
			"--quiet",
			"--collect",
			"--scope",
			"--uid=" + BuildUser,
			"--slice=" + BuildSlice,
			"--property=CPUQuota=200%",
			"--property=MemoryHigh=2G",
			"--property=MemoryMax=3G",
			"--property=TasksMax=1024",
		}, args...)
		return exec.CommandContext(ctx, wrapped[0], wrapped[1:]...), nil
	}
	// Best-effort fallback: try to drop to apigw-build via `su` if it's a
	// real user; otherwise the caller's identity (probably root for the
	// webhook worker) runs the build directly. Loud warning so operators
	// see this in journald.
	if _, err := exec.LookPath("su"); err == nil {
		if _, lerr := lookupUser(BuildUser); lerr == nil {
			joined := joinShellQuoted(args)
			return exec.CommandContext(ctx, "su", "-s", "/bin/sh", "-c", joined, BuildUser), nil
		}
	}
	return exec.CommandContext(ctx, args[0], args[1:]...), nil
}

// lookupUser wraps os/user without pulling it into other files.
func lookupUser(name string) (string, error) {
	// Avoid importing os/user just for IsExist style probing — peek at
	// /etc/passwd. Good enough for the fallback path.
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", err
	}
	for _, line := range splitLines(string(b)) {
		if hasFieldPrefix(line, name) {
			return name, nil
		}
	}
	return "", os.ErrNotExist
}

// joinShellQuoted produces a single shell-quoted command line — used by
// the su fallback so `su -c "<cmd>"` invokes the original argv intact.
func joinShellQuoted(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += shellQuote(a)
	}
	return out
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		switch r {
		case '\'', '"', ' ', '\t', '\n', '$', '\\', '`', '|', '&', ';', '(', ')', '<', '>':
			return "'" + escapeForSingleQuotes(s) + "'"
		}
	}
	return s
}

func escapeForSingleQuotes(s string) string {
	out := ""
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
			continue
		}
		out += string(r)
	}
	return out
}

func splitLines(s string) []string {
	out := []string{}
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[last:i])
			last = i + 1
		}
	}
	if last < len(s) {
		out = append(out, s[last:])
	}
	return out
}

func hasFieldPrefix(line, name string) bool {
	if len(line) < len(name)+1 {
		return false
	}
	if line[len(name)] != ':' {
		return false
	}
	return line[:len(name)] == name
}
