// Package policy embeds OPA (open-policy-agent) for Rego-based config
// admission control. Policies live in <config-dir>/policies/*.rego and
// are evaluated against every config mutation BEFORE it lands.
//
// Pattern: write a config change, build the "input" doc (action, before,
// after, actor), evaluate every package's "deny" rule. Any non-empty deny
// set blocks the change with the rule's message.
//
// Example policy:
//
//	package apigw.deploys
//	deny[msg] {
//	    input.action == "deploy.add"
//	    not input.after.health_check
//	    msg := "deploys must declare a health_check"
//	}
//
// At v0.5 we eval each policy file in its own Rego instance — cheap, and
// it makes policy reloading trivial (just re-scan the directory).
package policy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/open-policy-agent/opa/rego"
)

// Engine holds the loaded policy bundle. One per process; reload by
// calling LoadDir again.
type Engine struct {
	mu       sync.RWMutex
	policies []policy
}

type policy struct {
	Path  string
	Body  string
	Query string // package.deny query string
}

// Decision is the verdict of an Evaluate call. Denied=true blocks the
// change; the operator-facing messages are in Reasons (one per matching
// deny rule).
type Decision struct {
	Denied  bool
	Reasons []string
}

// NewEngine returns an empty Engine. Call LoadDir to populate.
func NewEngine() *Engine { return &Engine{} }

// LoadDir scans `dir` for *.rego files and replaces the loaded bundle.
// Returns the number of policies loaded.
func (e *Engine) LoadDir(dir string) (int, error) {
	policies := []policy{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".rego") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pkg := parsePackage(string(body))
		if pkg == "" {
			return fmt.Errorf("policy %s missing `package` declaration", path)
		}
		policies = append(policies, policy{
			Path:  path,
			Body:  string(body),
			Query: "data." + pkg + ".deny",
		})
		return nil
	})
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	e.policies = policies
	e.mu.Unlock()
	return len(policies), nil
}

// Evaluate runs every loaded policy against `input` and aggregates deny
// reasons. If no policies are loaded, the change is always allowed
// (fail-open) — this is the correct default for an opt-in feature.
func (e *Engine) Evaluate(ctx context.Context, input map[string]any) (Decision, error) {
	e.mu.RLock()
	policies := append([]policy(nil), e.policies...)
	e.mu.RUnlock()

	reasons := []string{}
	for _, p := range policies {
		r := rego.New(
			rego.Query(p.Query),
			rego.Module(p.Path, p.Body),
			rego.Input(input),
		)
		rs, err := r.Eval(ctx)
		if err != nil {
			return Decision{}, fmt.Errorf("policy %s: %w", p.Path, err)
		}
		if len(rs) == 0 || len(rs[0].Expressions) == 0 {
			continue
		}
		// rs[0].Expressions[0].Value is the deny set — []any of strings.
		denies, ok := rs[0].Expressions[0].Value.([]any)
		if !ok {
			continue
		}
		for _, d := range denies {
			if msg, ok := d.(string); ok && msg != "" {
				reasons = append(reasons, msg)
			}
		}
	}
	return Decision{Denied: len(reasons) > 0, Reasons: reasons}, nil
}

// parsePackage finds the `package x.y.z` declaration in a Rego file.
// Tolerates leading comments/blank lines but expects standard formatting.
func parsePackage(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return strings.TrimSpace(line[len("package "):])
		}
	}
	return ""
}
