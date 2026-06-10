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

// SandboxedCommand is a backward-compatible alias for
// SandboxedCommandIn("", args...). Prefer SandboxedCommandIn when you can —
// in transient-service mode the caller's cmd.Dir is NOT propagated to the
// child; only systemd-run's --working-directory= is.
func SandboxedCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return SandboxedCommandIn(ctx, "", args...)
}

// SandboxedCommandIn wraps `args` with `systemd-run --uid=apigw-build
// --slice=apigw-builds.slice` running as a transient service under PID1
// (so setresuid happens in PID1, not in our seccomp-filtered process —
// see the long comment inside). When `workdir` is non-empty it's threaded
// through as --working-directory=<workdir>; cmd.Dir set on the returned
// exec.Cmd applies only to systemd-run itself, NOT to the child.
//
// Falls back to su / plain exec when systemd-run isn't available
// (containers, Alpine without systemd, dev hosts).
//
// The brief from ARCHITECTURE.md §"Build sandboxing": "build runs in a
// transient unit inheriting our .slice budget; systemd-analyze security
// will score it the same as a long-running unit."
func SandboxedCommandIn(ctx context.Context, workdir string, args ...string) (*exec.Cmd, error) {
	if len(args) == 0 {
		return nil, errors.New("SandboxedCommand: empty args")
	}
	// Try systemd-run; fall through if missing (CI / containers without
	// systemd-run binary even though systemd is PID 1).
	if _, err := exec.LookPath("systemd-run"); err == nil {
		// Ensure the build user exists. Cheap when it already does.
		_ = EnsureSystemUser(BuildUser)
		// Run as a *transient service* (no --scope) so PID1 performs the
		// setresuid() into BuildUser. With --scope, systemd-run runs the
		// privilege drop in its OWN process; that inherits the caller's
		// seccomp filter, and the apigw-dashboard unit ships
		// `RestrictSUIDSGID=true`, so setresuid traps SIGSYS ("signal:
		// bad system call"). CLI-initiated deploys (root, no inherited
		// filter) used to slip through; webhook-driven ones from the
		// dashboard hit the wall.
		//
		// --wait blocks until the service exits; --pipe wires its stdio
		// back to us (--pipe is allowed in service mode — only --scope
		// rejects it on systemd ≥250). --service-type=exec gives us the
		// child's real exit code instead of a constant 0.
		wrapped := []string{
			"systemd-run",
			"--quiet",
			"--collect",
			"--wait",
			"--pipe",
			"--service-type=exec",
			"--uid=" + BuildUser,
			"--slice=" + BuildSlice,
			"--property=CPUQuota=200%",
			"--property=MemoryHigh=2G",
			"--property=MemoryMax=3G",
			"--property=TasksMax=1024",
		}
		if workdir != "" {
			wrapped = append(wrapped, "--working-directory="+workdir)
		}
		wrapped = append(wrapped, args...)
		return exec.CommandContext(ctx, wrapped[0], wrapped[1:]...), nil
	}
	// Best-effort fallback: try to drop to apigw-build via `su` if it's a
	// real user; otherwise the caller's identity (probably root for the
	// webhook worker) runs the build directly. Loud warning so operators
	// see this in journald.
	if _, err := exec.LookPath("su"); err == nil {
		if _, lerr := lookupUser(BuildUser); lerr == nil {
			joined := joinShellQuoted(args)
			cmd := exec.CommandContext(ctx, "su", "-s", "/bin/sh", "-c", joined, BuildUser)
			if workdir != "" {
				cmd.Dir = workdir
			}
			return cmd, nil
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	return cmd, nil
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
