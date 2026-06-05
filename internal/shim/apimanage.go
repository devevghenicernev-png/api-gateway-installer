// Package shim is the argv-translation layer that lets legacy
// `api-manage <subcommand> ...` invocations keep working after migrating to
// apigw. When the binary is started via the symlink at
// /usr/local/bin/api-manage, apigwcmd.Main detects argv[0] and routes
// through here.
//
// We deliberately translate at the argv layer (vs writing a cobra child
// command for every legacy verb): the translation is a 1:1 mapping that
// callers can read at a glance, and the deprecation banner makes it
// obvious to the operator they're using the compatibility path.
package shim

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// LegacyBinary is the file name the bash installer used. apigwcmd inspects
// filepath.Base(os.Args[0]) against this — anything else falls through
// to the regular cobra path.
const LegacyBinary = "api-manage"

// IsShim reports whether the running binary was invoked as api-manage.
func IsShim(argv0 string) bool {
	return filepath.Base(argv0) == LegacyBinary
}

// Translate rewrites the legacy argv into the cobra-shaped argv.
// argv[0] is replaced with "apigw" so cobra's usage output stays sane.
//
// Unknown legacy subcommands pass through unchanged — better to get a
// proper "unknown command" error from cobra than to lose the user's intent
// to an overly clever shim.
func Translate(argv []string) []string {
	out := []string{"apigw"}
	if len(argv) < 2 {
		return out
	}
	rest := argv[1:]

	switch rest[0] {
	// ----- API management -----
	case "add":
		// add <name> <port> [path]
		out = append(out, "api", "add")
		if len(rest) > 1 {
			out = append(out, rest[1])
		}
		if len(rest) > 2 {
			out = append(out, "--port", rest[2])
		}
		if len(rest) > 3 {
			out = append(out, "--path", rest[3])
		}
	case "remove":
		out = append(out, "api", "remove")
		out = append(out, rest[1:]...)
	case "list":
		out = append(out, "api", "list")
		out = append(out, rest[1:]...)
	case "enable":
		out = append(out, "api", "enable")
		out = append(out, rest[1:]...)
	case "disable":
		out = append(out, "api", "disable")
		out = append(out, rest[1:]...)
	case "reload":
		out = append(out, "api", "reload")
		out = append(out, rest[1:]...)

	// ----- Deployment management -----
	case "deploy":
		out = append(out, translateDeploy(rest[1:])...)

	// ----- Webhook -----
	case "webhook":
		out = append(out, translateWebhook(rest[1:])...)

	// ----- Dashboard -----
	case "dashboard":
		out = append(out, translateDashboard(rest[1:])...)

	// ----- AI -----
	case "ai":
		out = append(out, translateAI(rest[1:])...)

	// ----- System -----
	case "status", "doctor", "version":
		out = append(out, rest...)
	case "logs":
		// logs [service] → logs [--service <service>]
		out = append(out, "logs")
		if len(rest) > 1 {
			out = append(out, "--service", rest[1])
		}
	case "backup":
		out = append(out, "backup")
		if len(rest) > 1 {
			out = append(out, "--out", rest[1])
		}
	case "restore":
		out = append(out, "restore")
		out = append(out, rest[1:]...)

	default:
		// Unknown legacy verb — let cobra produce a clear error.
		out = append(out, rest...)
	}
	return out
}

// translateDeploy maps `deploy <verb> [args]`.
//
// Notable: legacy `deploy add <name> <repo> [branch] <port> [build] [start]`
// has POSITIONAL branch/port that may or may not be present. We probe the
// 4th positional — if it parses as an int we treat it as the port and the
// 3rd as the branch; otherwise we shift and assume legacy "branch missing".
func translateDeploy(args []string) []string {
	if len(args) == 0 {
		return []string{"deploy"}
	}
	switch args[0] {
	case "add":
		out := []string{"deploy", "add"}
		// args[1]=name, args[2]=repo, args[3]=branch|port, args[4]=port|build, args[5]=build|start, args[6]=start
		if len(args) > 1 {
			out = append(out, args[1])
		}
		if len(args) > 2 {
			out = append(out, "--repo", args[2])
		}
		// Heuristic for the optional branch positional.
		idx := 3
		if len(args) > idx {
			if isInt(args[idx]) {
				out = append(out, "--port", args[idx])
				idx++
			} else {
				out = append(out, "--branch", args[idx])
				idx++
				if len(args) > idx {
					out = append(out, "--port", args[idx])
					idx++
				}
			}
		}
		if len(args) > idx {
			out = append(out, "--build", args[idx])
			idx++
		}
		if len(args) > idx {
			out = append(out, "--start", args[idx])
		}
		return out
	case "setup-private":
		return []string{"deploy", "ssh-key"}
	case "remove", "list", "status", "run", "logs":
		return append([]string{"deploy", args[0]}, args[1:]...)
	}
	return append([]string{"deploy"}, args...)
}

func translateWebhook(args []string) []string {
	if len(args) == 0 {
		return []string{"webhook"}
	}
	return append([]string{"webhook"}, args...)
}

func translateDashboard(args []string) []string {
	if len(args) == 0 {
		return []string{"dashboard"}
	}
	return append([]string{"dashboard"}, args...)
}

func translateAI(args []string) []string {
	if len(args) == 0 {
		return []string{"ai"}
	}
	switch args[0] {
	case "pull":
		// Bash: `ai pull <model>` (provider implied = ollama).
		// New: `ai pull <provider> <model>`.
		if len(args) == 2 {
			return []string{"ai", "pull", "ollama", args[1]}
		}
	}
	return append([]string{"ai"}, args...)
}

// PrintDeprecation writes a one-line note to stderr each invocation.
// Loud but not noisy — same shape as gh's experimental-feature warnings.
func PrintDeprecation(w io.Writer) {
	fmt.Fprintln(w, "\x1b[33mapi-manage\x1b[0m is the legacy CLI — same commands now live under \x1b[1mapigw\x1b[0m. This shim will be removed in v2.0.")
}

func isInt(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if i == 0 && (c == '+' || c == '-') {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// TrimQuotedFlags is reserved for future flag-form normalisation
// (e.g. `--build="npm ci && npm run build"` → split out args). We don't
// need it yet — Go's argv parser already handles quoted values via
// os.Args splitting.
var _ = strings.TrimPrefix
