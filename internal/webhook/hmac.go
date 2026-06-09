package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifySignature returns true iff `header` is a valid HMAC-SHA256 over
// `body` with key `secret`.
//
// The header MUST be of the form "sha256=<hex>" per GitHub's spec.
// SHA-1 (`X-Hub-Signature`) is deprecated and we don't accept it.
//
// Crucial: comparison uses crypto/hmac.Equal — constant time. NEVER `==`
// or `bytes.Equal` here; both leak the prefix length on mismatch and have
// been exploited in the wild. This is one of the most common security
// mistakes in webhook code; see ARCHITECTURE.md §"Top 10 gotchas" #10.
//
// Returns false on any malformed input: missing prefix, non-hex body,
// length mismatch. We fail closed.
func VerifySignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// verifyAny tries each secret in turn (constant-time per candidate) and
// returns true if any verifies. Used by the server when a rotation grace
// is active and both the live + previous keys must be honoured.
func verifyAny(secrets [][]byte, body []byte, header string) bool {
	for _, s := range secrets {
		if VerifySignature(s, body, header) {
			return true
		}
	}
	return false
}

// Sign computes the HMAC header value for `body` with `secret`. Used by
// tests and the dashboard's "test webhook" feature.
//
// Output is the full "sha256=<hex>" string ready to put in the
// X-Hub-Signature-256 header.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
