package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SignatureScheme picks one of the supported webhook authentication
// flavours. Each scheme decides how to derive the expected bytes from
// (secret, body) and how to compare against the incoming header.
//
// Stays out of the existing GitHub-only VerifySignature so legacy
// callers don't shift behaviour; new callers go through VerifyAny.
type SignatureScheme string

const (
	// GitHub style: header value is `sha256=<hex>` of HMAC-SHA256(secret, body).
	// The same shape Bitbucket Cloud uses in `X-Hub-Signature-256`.
	SchemeGitHub SignatureScheme = "github"

	// Bitbucket Cloud uses the same wire shape as GitHub but the
	// header is `X-Hub-Signature-256`. Kept as a separate enum value
	// for documentation / dashboard surfacing — VerifyAny treats it
	// identically to GitHub.
	SchemeBitbucket SignatureScheme = "bitbucket"

	// GitLab sends a plain shared-secret in `X-Gitlab-Token` — no
	// HMAC, just constant-time equality. The Secret is the secret;
	// the body is unused for this scheme.
	SchemeGitLab SignatureScheme = "gitlab"

	// Generic HMAC-SHA256: header is the raw hex digest (no `sha256=`
	// prefix). Operator-friendly default for in-house publishers.
	SchemeGenericHMAC SignatureScheme = "hmac-sha256-hex"
)

// VerifyAny dispatches on scheme. Returns false on any malformed
// header / unknown scheme — fail closed.
func VerifyAny(scheme SignatureScheme, secret, body []byte, header string) bool {
	switch scheme {
	case "", SchemeGitHub, SchemeBitbucket:
		return VerifySignature(secret, body, header)
	case SchemeGitLab:
		// Constant-time compare on the raw secret. No HMAC; the body
		// is irrelevant because GitLab signs only by token presence.
		return hmac.Equal(secret, []byte(header))
	case SchemeGenericHMAC:
		got, err := hex.DecodeString(strings.TrimSpace(header))
		if err != nil {
			return false
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		return hmac.Equal(got, mac.Sum(nil))
	}
	return false
}

// SignWith returns the header value the publisher would put on a
// request signed by `scheme` — symmetric to VerifyAny. Used by the
// dashboard's "test webhook" feature and by integration tests.
func SignWith(scheme SignatureScheme, secret, body []byte) string {
	switch scheme {
	case "", SchemeGitHub, SchemeBitbucket:
		return Sign(secret, body)
	case SchemeGitLab:
		return string(secret)
	case SchemeGenericHMAC:
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}
	return ""
}
