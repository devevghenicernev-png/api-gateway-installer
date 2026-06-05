// Package deploy implements apigw's deployment engine: git fetch, runtime
// detection, build orchestration, systemd supervision, and zero-downtime
// nginx upstream swap.
//
// The architecture follows ARCHITECTURE.md §"Deploy Subsystem". Public
// entrypoints: Detect (runtime), Clone (fetch), Build (compile), Apply
// (the full deploy sequence including swap).
package deploy

import (
	"fmt"
	"os"
	"path/filepath"
)

// Runtime enumerates the buildpacks apigw natively supports.
type Runtime string

const (
	RuntimeAuto   Runtime = "auto"
	RuntimeNode   Runtime = "node"
	RuntimePython Runtime = "python"
	RuntimeGo     Runtime = "go"
	RuntimeDocker Runtime = "docker"
	RuntimeStatic Runtime = "static"
)

// DetectionResult records what runtime was picked and why. The "Reason"
// makes invisible heuristics visible — we surface it in `apigw deploy status`
// so a misclassification is easy to diagnose.
type DetectionResult struct {
	Runtime Runtime
	Reason  string // e.g. "matched: go.mod"
}

// Detect walks the deterministic decision tree from ARCHITECTURE.md
// §"Runtime detection". Order matters — Dockerfile always wins so power
// users can override; static is the fallback of last resort.
//
// The tree is intentionally short: Node / Python / Go / Docker / static.
// Adding Ruby or Rust is a one-line addition here plus a build.go default.
func Detect(root string) (DetectionResult, error) {
	if exists(root, "Dockerfile") {
		return DetectionResult{Runtime: RuntimeDocker, Reason: "matched: Dockerfile"}, nil
	}
	if exists(root, "go.mod") {
		return DetectionResult{Runtime: RuntimeGo, Reason: "matched: go.mod"}, nil
	}
	if exists(root, "package.json") {
		return DetectionResult{Runtime: RuntimeNode, Reason: "matched: package.json"}, nil
	}
	for _, p := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "poetry.lock"} {
		if exists(root, p) {
			return DetectionResult{Runtime: RuntimePython, Reason: "matched: " + p}, nil
		}
	}
	// Explicitly unsupported buildpacks — bail with an actionable error.
	for _, p := range []string{"Gemfile", "Cargo.toml"} {
		if exists(root, p) {
			return DetectionResult{}, fmt.Errorf("runtime detection: %s found but Ruby/Rust are out of scope (add a Dockerfile to override)", p)
		}
	}
	if exists(root, "index.html") || isDir(root, "public") || isDir(root, "dist") {
		return DetectionResult{Runtime: RuntimeStatic, Reason: "matched: index.html / public/ / dist/"}, nil
	}
	return DetectionResult{}, fmt.Errorf("runtime detection: no signal — add a Dockerfile, package.json, go.mod, or requirements.txt")
}

// Resolve takes a user-supplied runtime hint (auto|node|...|"") and a repo
// root, returning the final Runtime + reason. "auto" or "" runs Detect.
func Resolve(hint string, root string) (DetectionResult, error) {
	switch Runtime(hint) {
	case "", RuntimeAuto:
		return Detect(root)
	case RuntimeNode, RuntimePython, RuntimeGo, RuntimeDocker, RuntimeStatic:
		return DetectionResult{Runtime: Runtime(hint), Reason: "user-specified"}, nil
	default:
		return DetectionResult{}, fmt.Errorf("unknown runtime %q (use auto|node|python|go|docker|static)", hint)
	}
}

func exists(root, name string) bool {
	st, err := os.Stat(filepath.Join(root, name))
	return err == nil && !st.IsDir()
}

func isDir(root, name string) bool {
	st, err := os.Stat(filepath.Join(root, name))
	return err == nil && st.IsDir()
}
