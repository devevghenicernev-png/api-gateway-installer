package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetect_DecisionTree exercises each branch of the runtime detection
// tree in priority order. The on-disk fixture for each case is a
// minimal scaffold — just the file(s) the detector keys on, so we can
// audit at a glance which signal is firing.
func TestDetect_DecisionTree(t *testing.T) {
	cases := []struct {
		name    string
		files   []string // relative paths to touch
		dirs    []string
		want    Runtime
		wantErr bool
	}{
		{"dockerfile wins over package.json", []string{"Dockerfile", "package.json"}, nil, RuntimeDocker, false},
		{"dockerfile wins over go.mod", []string{"Dockerfile", "go.mod"}, nil, RuntimeDocker, false},
		{"go.mod beats package.json", []string{"go.mod", "package.json"}, nil, RuntimeGo, false},
		{"plain go.mod", []string{"go.mod"}, nil, RuntimeGo, false},
		{"plain package.json", []string{"package.json"}, nil, RuntimeNode, false},
		{"requirements.txt python", []string{"requirements.txt"}, nil, RuntimePython, false},
		{"pyproject.toml python", []string{"pyproject.toml"}, nil, RuntimePython, false},
		{"Pipfile python", []string{"Pipfile"}, nil, RuntimePython, false},
		{"Gemfile rejected", []string{"Gemfile"}, nil, "", true},
		{"Cargo.toml rejected", []string{"Cargo.toml"}, nil, "", true},
		{"static via index.html", []string{"index.html"}, nil, RuntimeStatic, false},
		{"static via public/", nil, []string{"public"}, RuntimeStatic, false},
		{"static via dist/", nil, []string{"dist"}, RuntimeStatic, false},
		{"nothing", nil, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, p := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res, err := Detect(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Detect should error; got runtime=%s reason=%q", res.Runtime, res.Reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if res.Runtime != tc.want {
				t.Fatalf("Detect.Runtime = %s, want %s (reason=%s)", res.Runtime, tc.want, res.Reason)
			}
			if res.Reason == "" {
				t.Fatal("Detect must always populate Reason")
			}
		})
	}
}

func TestResolve_UserOverride(t *testing.T) {
	cases := []struct {
		hint    string
		want    Runtime
		wantErr bool
	}{
		{"auto", "", true}, // empty repo + auto = no signal, errors
		{"", "", true},
		{"node", RuntimeNode, false},
		{"python", RuntimePython, false},
		{"go", RuntimeGo, false},
		{"docker", RuntimeDocker, false},
		{"static", RuntimeStatic, false},
		{"haskell", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.hint, func(t *testing.T) {
			res, err := Resolve(tc.hint, t.TempDir())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) should error; got %v", tc.hint, res)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.hint, err)
			}
			if res.Runtime != tc.want {
				t.Fatalf("Resolve(%q).Runtime = %s, want %s", tc.hint, res.Runtime, tc.want)
			}
		})
	}
}
