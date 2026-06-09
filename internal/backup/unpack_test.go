package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSafeTarget_RejectsTraversal verifies the path-traversal gate
// against the specific attack patterns: ../ injections, absolute paths,
// null bytes, and writes outside the allowed roots.
func TestSafeTarget_RejectsTraversal(t *testing.T) {
	cases := []string{
		"../etc/passwd",
		"etc/apigw/../../../etc/passwd",
		"/etc/passwd",
		"./etc/apigw/x",
		"var/lib/apigw/../../../root/.bashrc",
		"\x00",
		"/var/lib/apigw/legit", // absolute even when target IS allowed
		"home/user/.ssh/authorized_keys",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := safeTarget(c); err == nil {
				t.Fatalf("safeTarget(%q) should reject; returned nil error", c)
			}
		})
	}
}

func TestSafeTarget_AcceptsAllowedRoots(t *testing.T) {
	// Pin the test to the canonical Linux roots regardless of host OS.
	t.Setenv("APIGW_CONFIG_DIR", "/etc/apigw")
	t.Setenv("APIGW_STATE_DIR", "/var/lib/apigw")
	cases := []string{
		"etc/apigw/config.yaml",
		"var/lib/apigw/certs/example.com/fullchain.pem",
		"var/lib/apigw/acme/account.key",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := safeTarget(c); err != nil {
				t.Fatalf("safeTarget(%q) should accept; got %v", c, err)
			}
		})
	}
}

// TestUnpack_RejectsMaliciousArchive builds a tar.gz containing a
// "../../etc/passwd" entry and verifies Unpack aborts before writing it.
func TestUnpack_RejectsMaliciousArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")

	body := []byte("PWNED")
	mf := Manifest{
		SchemaVersion: SchemaVersion,
		CreatedAt:     time.Now().UTC(),
		Files: []FileSpec{
			{Path: "../../etc/passwd", Size: int64(len(body))},
		},
	}
	mfJSON, _ := json.MarshalIndent(mf, "", "  ")

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	// Malicious entry.
	if err := tw.WriteHeader(&tar.Header{Name: "../../etc/passwd", Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}

	// Manifest entry.
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: int64(len(mfJSON)), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(mfJSON); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()

	err = Unpack(archive, UnpackOptions{Force: true})
	if err == nil {
		t.Fatal("Unpack must reject traversal entry")
	}
	if !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("error must explain rejection; got %v", err)
	}
}
