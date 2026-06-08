// Package tuning renders apigw's main-context nginx directives
// (worker_processes / worker_cpu_affinity / worker_rlimit_nofile) into
// /etc/nginx/nginx.conf surrounded by a marker block.
//
// Why marker-based patching rather than a separate include file: nginx
// has no main-context `include` analogous to http-scope's conf.d. Other
// gateways solve this with `etc/nginx/main.d/`-style hooks they themselves
// added to nginx.conf at install time — that's effectively what we're
// doing, but the hook IS the rendered block.
//
// The block looks like:
//
//	# >>> apigw tuning >>> DO NOT EDIT — managed by `apigw tuning apply`
//	worker_processes  auto;
//	worker_cpu_affinity auto;
//	worker_rlimit_nofile 65535;
//	# <<< apigw tuning <<<
//
// First call writes a backup to nginx.conf.apigw-prev. `Revert` strips
// the block; backup is preserved so operators can diff.
package tuning

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	markerStart = "# >>> apigw tuning >>> DO NOT EDIT — managed by `apigw tuning apply`"
	markerEnd   = "# <<< apigw tuning <<<"

	// BackupExtension matches the convention used by internal/nginx.Manager.
	BackupExtension = ".apigw-prev"
)

// NginxConfPath is the location of the main nginx config. Override via
// the env var (matching paths.NginxConfPath()) — kept as a function so
// tests can swap it for a tempdir.
var NginxConfPath = func() string {
	if v := os.Getenv("APIGW_NGINX_CONF"); v != "" {
		return v
	}
	return "/etc/nginx/nginx.conf"
}

// Spec is the rendered directive set ready to drop into the marker block.
// Zero-valued fields are simply skipped (an empty Spec.Render() returns "").
type Spec struct {
	WorkerProcesses    string
	CPUAffinity        string
	WorkerRLimitNofile int
}

// Empty reports whether the spec would emit a no-op block.
func (s Spec) Empty() bool {
	return s.WorkerProcesses == "" && s.CPUAffinity == "" && s.WorkerRLimitNofile == 0
}

// Render returns just the lines inside the marker block (no markers).
// The first line is always present (`# apigw tuning — main context`); the
// remainder reflects which fields were set.
func (s Spec) Render() string {
	var b strings.Builder
	b.WriteString("# apigw tuning — main-context directives\n")
	if s.WorkerProcesses != "" {
		fmt.Fprintf(&b, "worker_processes      %s;\n", s.WorkerProcesses)
	}
	if s.CPUAffinity != "" {
		fmt.Fprintf(&b, "worker_cpu_affinity   %s;\n", s.CPUAffinity)
	}
	if s.WorkerRLimitNofile > 0 {
		fmt.Fprintf(&b, "worker_rlimit_nofile  %d;\n", s.WorkerRLimitNofile)
	}
	return b.String()
}

// blockRE matches the existing apigw tuning block (multiline, lazy body).
var blockRE = regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(markerStart) + `\n.*?^` + regexp.QuoteMeta(markerEnd) + `\n?`)

// Apply inserts or replaces the tuning block in NginxConfPath().
// The function is idempotent: running it twice with the same Spec is a
// no-op. Returns (changed, error) — `changed` is true only when the file
// content actually moved.
//
// On first edit a snapshot is written to <path>.apigw-prev so operators
// can `cp` it back if they want their original nginx.conf.
func Apply(spec Spec) (bool, error) {
	if spec.Empty() {
		return false, errors.New("tuning: nothing to apply (Spec is empty)")
	}
	path := NginxConfPath()
	body, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	wanted := markerStart + "\n" + spec.Render() + markerEnd + "\n"

	var out []byte
	if blockRE.Match(body) {
		out = blockRE.ReplaceAll(body, []byte(wanted))
	} else {
		out = insertAtTop(body, []byte(wanted))
	}
	if bytes.Equal(out, body) {
		return false, nil
	}
	// Snapshot the previous version exactly once — preserve operator's
	// pristine nginx.conf for diffing/restoration.
	bak := path + BackupExtension
	if _, statErr := os.Stat(bak); os.IsNotExist(statErr) {
		if werr := os.WriteFile(bak, body, 0o644); werr != nil {
			return false, fmt.Errorf("write backup %s: %w", bak, werr)
		}
	}
	if err := writeAtomic(path, out); err != nil {
		return false, err
	}
	return true, nil
}

// Revert removes the tuning block. Returns (changed, error). Idempotent.
func Revert() (bool, error) {
	path := NginxConfPath()
	body, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if !blockRE.Match(body) {
		return false, nil
	}
	out := blockRE.ReplaceAll(body, nil)
	if err := writeAtomic(path, out); err != nil {
		return false, err
	}
	return true, nil
}

// CurrentBlock returns the content (excluding markers) of the apigw
// tuning block currently in nginx.conf, or "" if absent. Useful for
// `apigw tuning show` so operators see what's live, not what would be.
func CurrentBlock() (string, error) {
	path := NginxConfPath()
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	m := blockRE.Find(body)
	if m == nil {
		return "", nil
	}
	// Strip the marker lines.
	s := string(m)
	s = strings.TrimPrefix(s, markerStart+"\n")
	s = strings.TrimSuffix(s, markerEnd+"\n")
	return s, nil
}

// insertAtTop puts `block` right after the leading comment / `user` /
// `worker_processes` lines but before `events {` — far enough up that
// nginx parses worker_* directives before forking, but past anything
// the operator put at the very top.
//
// Heuristic: find the first `events`/`http` directive and insert the block
// on the line above it. If neither exists (weird), insert at offset 0.
func insertAtTop(body, block []byte) []byte {
	re := regexp.MustCompile(`(?m)^(events|http)\s*\{`)
	loc := re.FindIndex(body)
	if loc == nil {
		return append(append([]byte{}, block...), body...)
	}
	// Walk back to the start of the line.
	insertAt := loc[0]
	out := make([]byte, 0, len(body)+len(block)+1)
	out = append(out, body[:insertAt]...)
	out = append(out, block...)
	out = append(out, '\n')
	out = append(out, body[insertAt:]...)
	return out
}

// writeAtomic mirrors the snapshot+rename pattern used by nginx.Manager.
func writeAtomic(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("tmp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
