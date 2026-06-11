package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/system"
)

// BuildSpec is the resolved build + start commands for a single release.
//
// If the user passed --build / --start on `apigw deploy add`, those win;
// otherwise we fall back to DefaultsFor(runtime). Either way the result is
// rendered into <release>/.apigw/start.sh so the systemd unit ExecStart is
// the same path regardless of runtime.
type BuildSpec struct {
	Runtime Runtime
	Build   string // empty = no build step
	Start   string // must produce a long-running foreground process
	Port    int    // exposed via APIGW_PORT env var

	// EnvFilePath is the on-disk path read by systemd's EnvironmentFile=
	// directive. Build does NOT read it — only the running unit does.
	EnvFilePath string
}

// DefaultsFor returns the canonical build/start commands per runtime.
//
// These are deliberately stripped down — we encourage explicit --build/--start
// for anything non-trivial. The defaults are "what does the average Express /
// Flask / Go HTTP server need".
func DefaultsFor(r Runtime) (build, start string) {
	switch r {
	case RuntimeNode:
		return "npm ci --omit=dev", "npm start"
	case RuntimePython:
		return "pip install --no-cache-dir -r requirements.txt", "python app.py"
	case RuntimeGo:
		return "go build -trimpath -o app ./...", "./app"
	case RuntimeDocker:
		// build/start are slot-filled at write time — depend on deploy name + sha.
		return "DOCKER_BUILD", "DOCKER_RUN"
	case RuntimeStatic:
		return "", "" // served directly by nginx; no process
	}
	return "", ""
}

// PrepareRelease writes <release>/.apigw/start.sh based on the spec and runs
// the build step.
//
// Returns the path to start.sh (what the systemd unit ExecStart points at)
// or an error if the build fails. Logs are streamed to logsink.
func PrepareRelease(ctx context.Context, deployName string, spec BuildSpec, releaseDir string, logsink io.Writer) (string, error) {
	if spec.Runtime == RuntimeStatic {
		return "", nil // nothing to build, nginx serves the dir directly
	}

	apigwDir := filepath.Join(releaseDir, ".apigw")
	if err := os.MkdirAll(apigwDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir .apigw: %w", err)
	}

	// Resolve docker placeholders.
	build, start := spec.Build, spec.Start
	if spec.Runtime == RuntimeDocker {
		tag := fmt.Sprintf("apigw-deploy/%s:%s", deployName, filepath.Base(releaseDir))
		if build == "" || build == "DOCKER_BUILD" {
			build = "docker build -t " + tag + " ."
		}
		if start == "" || start == "DOCKER_RUN" {
			start = fmt.Sprintf("docker run --rm --name apigw-%s -p %d:%d %s",
				deployName, spec.Port, spec.Port, tag)
		}
	}

	if build != "" {
		// No chown needed here: clone.go chowned the tree to the single
		// deploy user (apigw-run), the build runs as that same user, and
		// nothing hands ownership away — so a rebuild of an existing release
		// dir is already writable. (This used to re-chown to a separate
		// build user, which was the source of the EACCES-on-retry bug.)

		// Tee build output to a per-release build.log so failures are
		// debuggable after the fact. Before this, the build's stdout/stderr
		// went only to the SSE hub — if no browser tab was subscribed at
		// build time the output evaporated, and operators were stuck with
		// "exit status 243" and nothing else to go on.
		buildLogPath := filepath.Join(apigwDir, "build.log")
		buildLog, _ := os.OpenFile(buildLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		var combinedSink io.Writer = logsink
		if buildLog != nil {
			defer buildLog.Close()
			if logsink != nil {
				combinedSink = io.MultiWriter(logsink, buildLog)
			} else {
				combinedSink = buildLog
			}
		}

		if err := runBuild(ctx, releaseDir, build, spec.Port, combinedSink); err != nil {
			tail := lastLinesFromFile(buildLogPath, 30)
			if tail != "" {
				return "", fmt.Errorf("build failed: %w — last 30 lines of %s:\n%s",
					err, buildLogPath, tail)
			}
			return "", fmt.Errorf("build failed: %w (see %s for details)", err, buildLogPath)
		}
	}

	startPath := filepath.Join(apigwDir, "start.sh")
	if err := writeStartScript(startPath, releaseDir, start, spec.Port); err != nil {
		return "", err
	}
	return startPath, nil
}

// runBuild executes the build command in `cwd` with APIGW_PORT exported.
//
// Wraps the invocation as a `systemd-run --uid=apigw-run
// --slice=apigw-builds.slice` transient service when systemd-run is
// available — that gives us cgroup-enforced CPU/memory limits and runs the
// build non-root (same uid as the runtime, so no ownership handoff).
//
// On hosts without systemd-run (some CI images), system.SandboxedCommand
// falls back to `su -s /bin/sh -c <cmd> apigw-run`, then finally to the
// caller's own identity if even the user-switch isn't possible.
func runBuild(ctx context.Context, cwd, cmdStr string, port int, sink io.Writer) error {
	// SandboxedCommandIn threads cwd into systemd-run as
	// --working-directory=, which is the only way to influence the
	// transient service's CWD; cmd.Dir on the systemd-run process itself
	// would only affect systemd-run, not the spawned child.
	cmd, err := system.SandboxedCommandIn(ctx, cwd, "/bin/sh", "-c", cmdStr)
	if err != nil {
		return err
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"APIGW_PORT="+itoa(port),
		"PORT="+itoa(port),
		"NODE_ENV=production",
		"CI=1",
	)
	// Wrap whatever sink we use with a secret-scrubber so anything echoed by
	// the build (npm install dumping `process.env`, a shell `set -x`, etc.)
	// has its credential-looking values masked before reaching journald/SSE.
	//
	// When the caller didn't provide a sink (e.g. a programmatic caller
	// hooked deploy.Apply with Logsink unset), tee the build output to
	// os.Stderr live AND retain a tail buffer so the caller still has
	// something to print on error. Previously we only buffered + dumped
	// the last 40 lines on failure, which meant 0 live visibility — and
	// `npm install` jobs that hang for minutes looked like the deploy
	// was silently stuck.
	if sink != nil {
		scrub := newSecretScrubber(sink)
		cmd.Stdout = scrub
		cmd.Stderr = scrub
	} else {
		var buf bytes.Buffer
		scrub := newSecretScrubber(io.MultiWriter(os.Stderr, &buf))
		cmd.Stdout = scrub
		cmd.Stderr = scrub
		defer func() {
			// On non-nil error the caller wraps the message; we just attach the tail.
			if buf.Len() > 0 {
				fmt.Fprintln(os.Stderr, lastLines(buf.String(), 40))
			}
		}()
	}
	return cmd.Run()
}

func writeStartScript(path, releaseDir, start string, port int) error {
	if start == "" {
		return fmt.Errorf("start script: empty start command (set with --start)")
	}
	// For simple commands ("node server.js", "./app") `exec <cmd>` is best
	// — systemd sees the binary as the unit's leaf. For shell-construct
	// starts ("cd backend && npm start") we have to fall back to
	// `exec /bin/sh -c '<start>'` since `exec` only accepts an executable
	// path, not builtins or operators. Slight cost: extra /bin/sh in the
	// process tree; systemd's cgroup tracking still kills the whole tree
	// on stop.
	body := strings.Builder{}
	body.WriteString("#!/bin/sh\n")
	body.WriteString("set -e\n")
	body.WriteString("cd \"")
	body.WriteString(releaseDir)
	body.WriteString("\"\n")
	body.WriteString("export APIGW_PORT=")
	body.WriteString(itoa(port))
	body.WriteString("\n")
	body.WriteString("export PORT=")
	body.WriteString(itoa(port))
	body.WriteString("\n")
	if needsShellWrap(start) {
		body.WriteString("exec /bin/sh -c '")
		body.WriteString(strings.ReplaceAll(start, "'", `'\''`))
		body.WriteString("'\n")
	} else {
		body.WriteString("exec ")
		body.WriteString(start)
		body.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o755); err != nil {
		return err
	}
	return nil
}

// needsShellWrap returns true if `start` contains shell metacharacters or
// starts with a builtin — anything `exec` can't handle directly.
func needsShellWrap(start string) bool {
	if strings.ContainsAny(start, "&|;<>()`$\"") {
		return true
	}
	// Builtins exec can't resolve.
	first := start
	if i := strings.IndexByte(first, ' '); i > 0 {
		first = first[:i]
	}
	switch first {
	case "cd", "set", "export", "unset", "source", ".", "eval", "alias":
		return true
	}
	return false
}

func itoa(i int) string {
	// Avoid pulling strconv in a hot path that emits one int.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	idx := len(b)
	for i > 0 {
		idx--
		b[idx] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		idx--
		b[idx] = '-'
	}
	return string(b[idx:])
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func lastLinesFromFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return lastLines(string(b), n)
}
