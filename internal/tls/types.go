// Package tls is apigw's certificate subsystem.
//
// It owns: ACME account-key lifecycle, certificate issuance (HTTP-01 and
// DNS-01 via go-acme/lego/v4), self-signed fallback, on-disk cert storage,
// and renewal. nginx integration lives in internal/nginx — this package
// only produces PEM bytes and writes them to /var/lib/apigw/certs/.
//
// Why lego (not certbot, not CertMagic) is documented in ARCHITECTURE.md.
package tls

import (
	"path/filepath"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/paths"
)

// Strategy enumerates how a certificate is obtained.
//
// Stored in Config.TLS.Strategy. Empty/`"none"` means HTTP-only; nginx
// renders the non-TLS server block.
type Strategy string

const (
	StrategyNone        Strategy = "none"
	StrategyLetsEncrypt Strategy = "letsencrypt" // HTTP-01 challenge
	StrategyDuckDNS     Strategy = "duckdns"     // DNS-01 challenge via DuckDNS
	StrategySelfSigned  Strategy = "self-signed"
)

// IsValid reports whether s is one of the recognised strategies.
func (s Strategy) IsValid() bool {
	switch s {
	case StrategyNone, StrategyLetsEncrypt, StrategyDuckDNS, StrategySelfSigned:
		return true
	}
	return false
}

// IsACME reports whether the strategy requires the ACME client (lego).
func (s Strategy) IsACME() bool {
	return s == StrategyLetsEncrypt || s == StrategyDuckDNS
}

// ObtainRequest captures everything `apigw tls enable` needs.
//
// Validated by the calling command before being passed to Obtain. The fields
// are a union — strategy-specific fields are ignored for other strategies.
type ObtainRequest struct {
	Strategy Strategy

	// LE / DuckDNS shared:
	Email   string
	Staging bool // tests/CI flip this to lego.LetsEncryptStagingCA

	// Let's Encrypt + DuckDNS — the public host name(s).
	Domains []string

	// DuckDNS-only.
	DuckDNSToken string

	// DNSPropagationTimeout overrides the lego default (120s) for DNS-01
	// solvers. Zero keeps the default. Bump on slow registrars / when LE
	// rate-limit retries push the verification window past 2 min.
	DNSPropagationTimeout time.Duration

	// Self-signed-only.
	CommonName string
	ValidFor   time.Duration // default 365d
}

// CertInfo is the metadata block returned by Status. Used by `apigw tls
// status`, the dashboard expiry countdown, and renewal scheduling.
type CertInfo struct {
	Domain      string    `json:"domain"`
	Strategy    Strategy  `json:"strategy"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DaysLeft    int       `json:"days_left"`
	SerialHex   string    `json:"serial_hex"`
	Fingerprint string    `json:"fingerprint_sha256"`
}

// RenewalThreshold is "how close to expiry before we re-issue".
//
// 30 days is the industry default (certbot uses the same). Tighter risks
// flapping on transient ACME outages; looser leaves no buffer for failure.
const RenewalThreshold = 30 * 24 * time.Hour

// BaseDir returns the root for apigw's TLS state (paths.StateDir()).
func BaseDir() string { return paths.StateDir() }

// AccountDir holds the long-lived ACME account key + registration.
func AccountDir() string { return filepath.Join(BaseDir(), "acme") }

// CertDir is the parent of per-domain subdirectories.
func CertDir() string { return filepath.Join(BaseDir(), "certs") }

// AcmeWebrootDir is what nginx serves /.well-known/acme-challenge/ from
// during HTTP-01 renewals. Created by `apigw tls enable`.
func AcmeWebrootDir() string { return filepath.Join(BaseDir(), "acme-webroot") }
