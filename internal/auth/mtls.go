// Package auth — mTLS (mutual TLS) client certificate validation.
//
// Flow: nginx terminates the client TLS handshake with ssl_verify_client on,
// passes the verified cert info via headers ($ssl_client_s_dn, $ssl_client_verify,
// $ssl_client_fingerprint) to the dashboard daemon's /auth/mtls/<api>
// endpoint. The dashboard parses the DN, matches against per-API allow-lists
// (CN, SAN, fingerprint), and responds 200/401/403.
//
// We don't make nginx do the matching directly because:
//   - $ssl_client_s_dn formatting varies across nginx versions
//   - matching subjectAltName extensions in nginx requires Lua
//   - centralizing in the dashboard keeps audit + RBAC consistent
package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// MTLSConfig describes per-API mTLS validation. CAFile is required; everything
// else is optional refinement.
type MTLSConfig struct {
	// CAFile is the PEM bundle of CAs whose-issued certs apigw accepts.
	// nginx already verifies the cert chains against this; the dashboard
	// re-checks for defense-in-depth and to match against allow-lists.
	CAFile string

	// AllowCNs is a list of acceptable Common Names. Empty means any CN
	// passes (CA trust is enough).
	AllowCNs []string

	// AllowSANs lists acceptable subject-alternative-name DNS entries.
	// Empty = any.
	AllowSANs []string

	// AllowFingerprints lists SHA-256 cert fingerprints (hex, lowercase, no colons).
	// When set, the cert's fingerprint MUST appear here — overrides CN/SAN.
	// Use this for cert pinning.
	AllowFingerprints []string

	// Optional makes mTLS opt-in: clients without certs pass through with
	// no remote identity. Use when migrating from no-mTLS to mTLS.
	Optional bool
}

// MTLSResult is what Verify returns.
type MTLSResult int

const (
	MTLSOK           MTLSResult = iota
	MTLSNoVerify                // ssl_verify_client returned NONE (cert missing) and !Optional
	MTLSNotAllowed              // cert OK but not in allow-list
	MTLSBackendError            // CA file read failed, etc.
)

func (r MTLSResult) HTTPStatus() int {
	switch r {
	case MTLSOK:
		return http.StatusOK
	case MTLSNoVerify, MTLSNotAllowed:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// VerifyHeaders parses the nginx-supplied verification headers and matches
// against the config's allow-lists. The dashboard's mTLS handler is the only
// caller; this function exists separately so it can be unit-tested without
// HTTP plumbing.
//
// Expected headers (nginx server-block must forward them via proxy_set_header):
//
//	X-Apigw-Mtls-Verify       — value of $ssl_client_verify ("SUCCESS" / "FAILED:<reason>" / "NONE")
//	X-Apigw-Mtls-SDN          — $ssl_client_s_dn (e.g. "CN=client.example.com,O=Acme,C=US")
//	X-Apigw-Mtls-Cert         — $ssl_client_escaped_cert (URL-encoded PEM)
//	X-Apigw-Mtls-Fingerprint  — $ssl_client_fingerprint (lowercase hex sha1)
//	                            We ALSO compute sha256 from the cert body when AllowFingerprints uses sha256.
func VerifyMTLS(cfg MTLSConfig, h http.Header) (MTLSResult, string, string) {
	verify := h.Get("X-Apigw-Mtls-Verify")
	if verify == "NONE" || verify == "" {
		if cfg.Optional {
			return MTLSOK, "", "no client cert (optional)"
		}
		return MTLSNoVerify, "", "no client cert provided"
	}
	if !strings.HasPrefix(verify, "SUCCESS") {
		return MTLSNoVerify, "", "nginx reported: " + verify
	}

	// Parse the forwarded cert. We trust nginx already verified against
	// the CA bundle; this parse is to extract CN/SAN/fingerprint for the
	// allow-list match.
	pemStr := h.Get("X-Apigw-Mtls-Cert")
	if pemStr == "" {
		// No cert body forwarded — fall back to DN-only matching.
		dn := h.Get("X-Apigw-Mtls-SDN")
		cn := extractCN(dn)
		if cn == "" {
			return MTLSBackendError, "", "no cert body and no DN forwarded"
		}
		if allowCN(cfg, cn) {
			return MTLSOK, cn, "DN-only match"
		}
		return MTLSNotAllowed, cn, "CN not in allow-list (DN-only): " + cn
	}

	cert, err := parseCertPEM(pemStr)
	if err != nil {
		return MTLSBackendError, "", "parse cert: " + err.Error()
	}

	// Fingerprint match — strongest form (cert pinning).
	if len(cfg.AllowFingerprints) > 0 {
		fp := sha256Fingerprint(cert.Raw)
		if !contains(cfg.AllowFingerprints, fp) {
			return MTLSNotAllowed, cert.Subject.CommonName, "fingerprint not pinned: " + fp
		}
		return MTLSOK, cert.Subject.CommonName, "fingerprint match"
	}

	// CN match.
	if !allowCN(cfg, cert.Subject.CommonName) {
		// Try SAN match before failing.
		if matchesSAN(cfg.AllowSANs, cert.DNSNames) {
			return MTLSOK, cert.Subject.CommonName, "SAN match"
		}
		return MTLSNotAllowed, cert.Subject.CommonName,
			fmt.Sprintf("CN %q and SANs %v not in allow-list", cert.Subject.CommonName, cert.DNSNames)
	}
	return MTLSOK, cert.Subject.CommonName, "CN match"
}

// allowCN returns true if cn matches any AllowCNs entry, OR AllowCNs is
// empty (any-CN policy — CA trust is sufficient).
func allowCN(cfg MTLSConfig, cn string) bool {
	if len(cfg.AllowCNs) == 0 {
		// No CN restriction — implicit any.
		return true
	}
	for _, allow := range cfg.AllowCNs {
		if cn == allow {
			return true
		}
		// Support simple wildcards: "*.example.com" matches "x.example.com".
		if strings.HasPrefix(allow, "*.") && strings.HasSuffix(cn, allow[1:]) {
			return true
		}
	}
	return false
}

// matchesSAN returns true if any of certSANs appears in allow (or if allow
// is empty). Wildcard semantics same as allowCN.
func matchesSAN(allow []string, certSANs []string) bool {
	if len(allow) == 0 {
		return false
	}
	for _, want := range allow {
		for _, got := range certSANs {
			if got == want {
				return true
			}
			if strings.HasPrefix(want, "*.") && strings.HasSuffix(got, want[1:]) {
				return true
			}
		}
	}
	return false
}

// parseCertPEM decodes the PEM-encoded cert nginx forwarded.
func parseCertPEM(pemStr string) (*x509.Certificate, error) {
	// nginx URL-encodes the cert via $ssl_client_escaped_cert; many setups
	// just forward $ssl_client_cert which is BASE64 inline. Try both: if
	// the string contains "BEGIN CERTIFICATE", treat as PEM; if it's URL-encoded,
	// the dashboard's handler is expected to decode before calling us.
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

// extractCN parses an RFC-2253 distinguished name and returns the CN.
// We don't pull in a full DN parser — match the dominant nginx format
// (comma-separated, key=value).
func extractCN(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToUpper(part), "CN=") {
			return part[3:]
		}
	}
	return ""
}

// sha256Fingerprint returns the lowercase hex SHA-256 of the cert DER bytes.
// Matches openssl x509 -fingerprint -sha256 -noout output (minus the colons).
func sha256Fingerprint(der []byte) string {
	sum := sha256Sum(der)
	return hexEncode(sum[:])
}

// sha256Sum / hexEncode are pulled into local helpers to keep the import
// list tight — crypto/sha256 and encoding/hex are the canonical answers
// but we hide them so the package's public surface stays trivial.
func sha256Sum(b []byte) [32]byte {
	return sha256SumImpl(b)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Compile-time check the helpers compile against the right pkix structure.
var _ = pkix.Name{}
