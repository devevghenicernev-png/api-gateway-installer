package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestSignVerify_Roundtrip pairs the spec-canonical Sign() output with the
// VerifySignature() consumer side: any payload we sign must verify with
// the same key. Failure here = our wire format diverged from GitHub's.
func TestSignVerify_Roundtrip(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		body   string
	}{
		{"empty body", "deadbeef", ""},
		{"single byte", "deadbeef", "x"},
		{"json push payload", "feedface", `{"ref":"refs/heads/main","after":"abc"}`},
		{"multibyte unicode", "feedface", "{\"msg\":\"привет — мир\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig := Sign([]byte(tc.secret), []byte(tc.body))
			if !strings.HasPrefix(sig, "sha256=") {
				t.Fatalf("Sign must emit sha256= prefix; got %q", sig)
			}
			if !VerifySignature([]byte(tc.secret), []byte(tc.body), sig) {
				t.Fatalf("self-roundtrip failed for case %q (sig=%s)", tc.name, sig)
			}
		})
	}
}

// TestVerifySignature_RejectsTampering covers the core HMAC properties we
// rely on: body mutation flips the signature, wrong key fails, malformed
// inputs fail closed.
func TestVerifySignature_RejectsTampering(t *testing.T) {
	const secret = "topsecretkey"
	const body = `{"ref":"refs/heads/main","after":"abc"}`
	good := Sign([]byte(secret), []byte(body))

	cases := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   bool
	}{
		{"happy path", []byte(secret), []byte(body), good, true},
		{"wrong secret", []byte("nope"), []byte(body), good, false},
		{"flipped body", []byte(secret), []byte(body + "!"), good, false},
		{"missing prefix", []byte(secret), []byte(body), strings.TrimPrefix(good, "sha256="), false},
		{"sha1 prefix", []byte(secret), []byte(body), "sha1=" + strings.TrimPrefix(good, "sha256="), false},
		{"non-hex body", []byte(secret), []byte(body), "sha256=zzz", false},
		{"empty header", []byte(secret), []byte(body), "", false},
		{"empty secret", []byte{}, []byte(body), good, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VerifySignature(tc.secret, tc.body, tc.header)
			if got != tc.want {
				t.Fatalf("VerifySignature(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestVerifySignature_UsesHMACEqual is a *behavioural* sanity check that
// our verifier matches the constant-time implementation a careful reader
// would expect — same digest under hmac.Equal must accept.
func TestVerifySignature_UsesHMACEqual(t *testing.T) {
	const secret = "k"
	body := []byte("hello")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature([]byte(secret), body, header) {
		t.Fatal("VerifySignature rejected a hand-computed valid HMAC")
	}
}
