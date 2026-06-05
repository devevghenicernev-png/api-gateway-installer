package shim

import (
	"reflect"
	"testing"
)

func TestTranslate_API(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"add with port + path",
			[]string{"api-manage", "add", "hello", "8080", "/hello"},
			[]string{"apigw", "api", "add", "hello", "--port", "8080", "--path", "/hello"},
		},
		{
			"add without path",
			[]string{"api-manage", "add", "hello", "8080"},
			[]string{"apigw", "api", "add", "hello", "--port", "8080"},
		},
		{
			"remove",
			[]string{"api-manage", "remove", "hello"},
			[]string{"apigw", "api", "remove", "hello"},
		},
		{
			"list",
			[]string{"api-manage", "list"},
			[]string{"apigw", "api", "list"},
		},
		{
			"enable / disable / reload",
			[]string{"api-manage", "enable", "h"},
			[]string{"apigw", "api", "enable", "h"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Translate(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Translate(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTranslate_DeployHeuristic(t *testing.T) {
	// `deploy add <name> <repo> [branch] <port> [build] [start]` —
	// the heuristic at internal/shim/apimanage.go uses isInt() on the
	// 4th positional to decide whether branch was supplied.
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"no branch (branch omitted, port at pos 3)",
			[]string{"api-manage", "deploy", "add", "x", "https://repo", "3000"},
			[]string{"apigw", "deploy", "add", "x", "--repo", "https://repo", "--port", "3000"},
		},
		{
			"with branch",
			[]string{"api-manage", "deploy", "add", "x", "https://repo", "feat", "3000"},
			[]string{"apigw", "deploy", "add", "x", "--repo", "https://repo", "--branch", "feat", "--port", "3000"},
		},
		{
			"with branch + build + start",
			[]string{"api-manage", "deploy", "add", "x", "https://repo", "main", "3000", "npm ci", "npm start"},
			[]string{"apigw", "deploy", "add", "x", "--repo", "https://repo", "--branch", "main", "--port", "3000", "--build", "npm ci", "--start", "npm start"},
		},
		{"setup-private", []string{"api-manage", "deploy", "setup-private"}, []string{"apigw", "deploy", "ssh-key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Translate(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Translate(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTranslate_AIPullProviderImplied(t *testing.T) {
	// Legacy bash: `ai pull <model>` (provider implicit = ollama).
	got := Translate([]string{"api-manage", "ai", "pull", "llama3"})
	want := []string{"apigw", "ai", "pull", "ollama", "llama3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Translate ai pull = %v, want %v", got, want)
	}
}

func TestTranslate_UnknownVerbPassesThrough(t *testing.T) {
	got := Translate([]string{"api-manage", "frobnicate", "x"})
	want := []string{"apigw", "frobnicate", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Translate unknown verb = %v, want %v", got, want)
	}
}

func TestIsShim(t *testing.T) {
	cases := map[string]bool{
		"/usr/local/bin/api-manage": true,
		"api-manage":                true,
		"/usr/local/bin/apigw":      false,
		"apigw":                     false,
		"./api-manage":              true,
	}
	for argv0, want := range cases {
		if got := IsShim(argv0); got != want {
			t.Errorf("IsShim(%q) = %v, want %v", argv0, got, want)
		}
	}
}
