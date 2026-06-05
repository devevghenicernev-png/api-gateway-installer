package deploy

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func runScrub(t *testing.T, input string) string {
	t.Helper()
	var buf bytes.Buffer
	s := newSecretScrubber(&buf)
	if _, err := io.WriteString(s, input); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return buf.String()
}

func TestScrubKEYVALUE(t *testing.T) {
	in := "AWS_SECRET_ACCESS_KEY=abcdef1234\n"
	out := runScrub(t, in)
	if strings.Contains(out, "abcdef1234") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, "AWS_SECRET_ACCESS_KEY=***") {
		t.Fatalf("expected mask, got %q", out)
	}
}

func TestScrubColonForm(t *testing.T) {
	in := "DATABASE_PASSWORD: hunter2-correct-horse\n"
	out := runScrub(t, in)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %q", out)
	}
}

func TestScrubAuthorizationHeader(t *testing.T) {
	in := "> Authorization: Bearer eyJhbGc.payload.sig\n"
	out := runScrub(t, in)
	if strings.Contains(out, "eyJhbGc") {
		t.Fatalf("token leaked: %q", out)
	}
	if !strings.Contains(out, "Authorization: ***") {
		t.Fatalf("expected mask, got %q", out)
	}
}

func TestScrubURLCreds(t *testing.T) {
	in := "cloning from https://alice:supersecret@github.com/me/repo\n"
	out := runScrub(t, in)
	if strings.Contains(out, "supersecret") {
		t.Fatalf("password leaked: %q", out)
	}
	if !strings.Contains(out, "https://alice:***@github.com") {
		t.Fatalf("expected mask, got %q", out)
	}
}

func TestScrubPreservesNonSecretLines(t *testing.T) {
	in := "info: starting build\nupgrading package vue@3.5.10\n"
	out := runScrub(t, in)
	if out != in {
		t.Fatalf("non-secret line was modified: %q → %q", in, out)
	}
}

func TestScrubLineBuffering(t *testing.T) {
	// Write byte-by-byte to confirm we buffer up to newline before scrubbing
	// so a regex doesn't fragment across Write boundaries.
	var buf bytes.Buffer
	s := newSecretScrubber(&buf)
	in := "TOKEN=leaky-secret\n"
	for i := 0; i < len(in); i++ {
		_, _ = s.Write([]byte{in[i]})
	}
	if got := buf.String(); strings.Contains(got, "leaky-secret") {
		t.Fatalf("byte-by-byte write leaked secret: %q", got)
	}
}

func TestScrubFlushPartialLine(t *testing.T) {
	var buf bytes.Buffer
	s := newSecretScrubber(&buf)
	// No trailing newline — partial line. Flush should still scrub.
	_, _ = io.WriteString(s, "PASSWORD=oops-no-newline")
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if strings.Contains(buf.String(), "oops-no-newline") {
		t.Fatalf("flush leaked: %q", buf.String())
	}
}

func TestScrubMultipleSecretsOnOneLine(t *testing.T) {
	in := "DB_PASSWORD=pw1 AND API_KEY=key2\n"
	out := runScrub(t, in)
	if strings.Contains(out, "pw1") || strings.Contains(out, "key2") {
		t.Fatalf("multi-secret leaked: %q", out)
	}
}

func TestScrubCaseInsensitive(t *testing.T) {
	in := "lower_password=pw1\nAUTHORIZATION:Bearer xyz\n"
	out := runScrub(t, in)
	if strings.Contains(out, "pw1") || strings.Contains(out, "xyz") {
		t.Fatalf("case variants leaked: %q", out)
	}
}
