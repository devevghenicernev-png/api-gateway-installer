// Package diag is apigw's diagnostic framework. `apigw doctor` is the
// canonical caller; `apigw status` uses a subset.
//
// A Check is an idempotent, side-effect-free function that returns a
// Result with a level (ok/info/warn/fail), a one-line message, and an
// optional "Fix" — the exact command an operator should run. Checks
// should be cheap (sub-second); long probes belong in `apigw status`.
package diag

import (
	"context"
	"sort"
	"sync"
)

// Level grades a check outcome. Maps to exit codes in `apigw doctor`:
// LevelOK/Info → 0, LevelWarn → 1, LevelFail → 2.
type Level int

const (
	LevelOK   Level = iota // explicit pass
	LevelInfo              // diagnostic info, not a problem
	LevelWarn              // worth attention, not blocking
	LevelFail              // broken — fix before running production
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "ok"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelFail:
		return "fail"
	}
	return "?"
}

// Result is what a Check returns. Message is one short line (no trailing
// newline). Fix is the exact command an operator should run, or "" if
// there is no automatic remediation. Details surfaces extra context the
// `--json` flag passes through.
type Result struct {
	Level   Level             `json:"level"`
	Name    string            `json:"name"`
	Message string            `json:"message"`
	Fix     string            `json:"fix,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// Check is one diagnostic. Pure function in spirit — Run() must not mutate
// state. ID is stable across binary versions for scripting.
type Check struct {
	ID          string
	Description string
	Run         func(context.Context) Result
}

// Runner executes a set of checks in parallel. Each check has its own
// context derived from the runner's; cancelling Run() cancels them all.
//
// Parallel because checks are I/O-bound (TCP probes, file stats, exec).
// Order in the returned slice matches the order of the input.
type Runner struct {
	Checks []Check
}

// Run executes all checks concurrently and returns results in the original
// order. Panics in a check are recovered and reported as LevelFail.
func (r *Runner) Run(ctx context.Context) []Result {
	out := make([]Result, len(r.Checks))
	var wg sync.WaitGroup
	for i, c := range r.Checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					out[i] = Result{
						Level:   LevelFail,
						Name:    c.ID,
						Message: "check panicked",
						Details: map[string]string{"panic": toString(rec)},
					}
				}
			}()
			res := c.Run(ctx)
			if res.Name == "" {
				res.Name = c.ID
			}
			out[i] = res
		}(i, c)
	}
	wg.Wait()
	return out
}

// Worst returns the highest Level seen in results. Used to decide exit code.
func Worst(results []Result) Level {
	worst := LevelOK
	for _, r := range results {
		if r.Level > worst {
			worst = r.Level
		}
	}
	return worst
}

// SortByLevelThenName arranges results so failures float to the top —
// the operator's eye lands on what to fix first.
func SortByLevelThenName(results []Result) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Level != results[j].Level {
			return results[i].Level > results[j].Level
		}
		return results[i].Name < results[j].Name
	})
}

func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case error:
		return s.Error()
	}
	return ""
}
