package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		available string
		want      bool
	}{
		{"dev vs anything", "dev", "v0.1.0", true},
		{"empty current", "", "0.1.0", true},
		{"equal", "0.1.0", "0.1.0", false},
		{"equal with v prefix", "v0.1.0", "0.1.0", false},
		{"patch up", "0.1.0", "0.1.1", true},
		{"patch down", "0.1.1", "0.1.0", false},
		{"minor up", "0.1.5", "0.2.0", true},
		{"minor down", "0.2.0", "0.1.5", false},
		{"major up", "0.9.99", "1.0.0", true},
		{"minor up across width", "0.9.0", "0.10.0", true}, // numeric, not lex
		{"prerelease ignored equal", "1.0.0", "1.0.0-rc1", false},
		{"prerelease ignored newer", "1.0.0", "1.0.1-rc1", true},
		{"build metadata stripped", "1.0.0", "1.0.1+abcd", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsNewer(tc.current, tc.available)
			if got != tc.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tc.current, tc.available, got, tc.want)
			}
		})
	}
}

func TestNormaliseVersion(t *testing.T) {
	cases := map[string]string{
		"v0.1.0":  "0.1.0",
		"0.1.0":   "0.1.0",
		"":        "",
		"v":       "",
		"v1.2.3a": "1.2.3a",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := NormaliseVersion(in); got != want {
				t.Fatalf("NormaliseVersion(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
