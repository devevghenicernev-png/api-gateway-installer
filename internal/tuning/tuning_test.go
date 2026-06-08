package tuning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// patchPath redirects NginxConfPath() at a temp file and seeds the file
// with the given body. Returns the path + restore func.
func patchPath(t *testing.T, seed string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := NginxConfPath
	NginxConfPath = func() string { return path }
	return path, func() { NginxConfPath = prev }
}

const seedDefault = `user www-data;
worker_processes auto;

events {
    worker_connections 768;
}

http {
    sendfile on;
    include /etc/nginx/conf.d/*.conf;
    include /etc/nginx/sites-enabled/*;
}
`

func TestApply_InsertsBeforeEvents(t *testing.T) {
	path, restore := patchPath(t, seedDefault)
	defer restore()

	changed, err := Apply(Spec{WorkerProcesses: "auto", CPUAffinity: "auto", WorkerRLimitNofile: 65535})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !changed {
		t.Fatal("first Apply must report changed=true")
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, markerStart) || !strings.Contains(s, markerEnd) {
		t.Fatalf("markers missing:\n%s", s)
	}
	if !strings.Contains(s, "worker_cpu_affinity   auto;") {
		t.Fatalf("cpu_affinity directive missing:\n%s", s)
	}
	// Insertion point: marker should appear BEFORE `events {`.
	mIdx := strings.Index(s, markerStart)
	eIdx := strings.Index(s, "events {")
	if mIdx < 0 || eIdx < 0 || mIdx > eIdx {
		t.Fatalf("expected markers before events { block (mIdx=%d eIdx=%d)", mIdx, eIdx)
	}
	// Snapshot was written.
	if _, err := os.Stat(path + BackupExtension); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestApply_Idempotent(t *testing.T) {
	_, restore := patchPath(t, seedDefault)
	defer restore()
	spec := Spec{WorkerProcesses: "auto", CPUAffinity: "auto"}
	if _, err := Apply(spec); err != nil {
		t.Fatal(err)
	}
	changed, err := Apply(spec)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second Apply with same Spec must be no-op (changed=false)")
	}
}

func TestApply_ReplacesExistingBlock(t *testing.T) {
	path, restore := patchPath(t, seedDefault)
	defer restore()
	if _, err := Apply(Spec{WorkerProcesses: "4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Spec{WorkerProcesses: "8", CPUAffinity: "auto"}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	s := string(out)
	if strings.Count(s, markerStart) != 1 {
		t.Fatalf("marker should appear exactly once after replacement:\n%s", s)
	}
	if !strings.Contains(s, "worker_processes      8;") {
		t.Fatalf("expected worker_processes 8 after replace:\n%s", s)
	}
}

func TestApply_EmptySpecRejected(t *testing.T) {
	_, restore := patchPath(t, seedDefault)
	defer restore()
	if _, err := Apply(Spec{}); err == nil {
		t.Fatal("expected error for empty spec")
	}
}

func TestRevert_RemovesBlock(t *testing.T) {
	path, restore := patchPath(t, seedDefault)
	defer restore()
	if _, err := Apply(Spec{WorkerProcesses: "auto"}); err != nil {
		t.Fatal(err)
	}
	changed, err := Revert()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Revert must report changed=true the first time")
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), markerStart) {
		t.Fatalf("marker still present after revert:\n%s", out)
	}
	// Second revert is a no-op.
	changed, err = Revert()
	if err != nil || changed {
		t.Fatalf("second revert: changed=%v err=%v", changed, err)
	}
}

func TestCurrentBlock(t *testing.T) {
	_, restore := patchPath(t, seedDefault)
	defer restore()
	if got, _ := CurrentBlock(); got != "" {
		t.Fatalf("expected empty before Apply, got %q", got)
	}
	if _, err := Apply(Spec{WorkerProcesses: "auto", WorkerRLimitNofile: 65535}); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentBlock()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "worker_rlimit_nofile  65535;") {
		t.Fatalf("CurrentBlock missing directive:\n%s", got)
	}
	if strings.Contains(got, markerStart) {
		t.Fatalf("CurrentBlock should strip markers, got:\n%s", got)
	}
}

func TestApply_NoEventsOrHttp(t *testing.T) {
	// Bare-bones nginx.conf with no events/http (synthetic edge case).
	path, restore := patchPath(t, "# weird config\n")
	defer restore()
	if _, err := Apply(Spec{WorkerProcesses: "auto"}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	s := string(out)
	if !strings.HasPrefix(s, markerStart) {
		t.Fatalf("expected marker at top:\n%s", s)
	}
}
