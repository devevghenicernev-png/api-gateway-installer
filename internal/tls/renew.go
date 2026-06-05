package tls

import (
	"fmt"
	"time"
)

// RenewResult is the per-domain outcome of a renewal pass.
type RenewResult struct {
	Domain    string
	Strategy  Strategy
	Renewed   bool   // false means "still valid, skipped"
	Error     error  // non-nil if obtain failed
	DaysLeft  int    // days until expiry AFTER the pass
	OldExpiry time.Time
	NewExpiry time.Time
}

// RenewOptions controls a renewal pass.
type RenewOptions struct {
	// Force re-obtain even if the cert isn't near expiry. Mapped to
	// `apigw tls renew --force`.
	Force bool

	// DryRun reports what would happen without contacting the ACME server.
	// Mapped to `apigw tls renew --dry-run` — same flag certbot ships.
	DryRun bool

	// Threshold overrides the default 30 day window. Zero = use default.
	Threshold time.Duration

	// Email + Staging carry through to ACME obtains for certs whose meta
	// doesn't already have an account configured. Optional.
	Email   string
	Staging bool

	// DuckDNSToken is needed if any cert uses StrategyDuckDNS. Pulled from
	// config.TLS.DuckDNSToken in the calling command.
	DuckDNSToken string
}

// RenewAll iterates every stored cert and renews those near expiry.
//
// The atomic swap in StoreCert is what keeps nginx safe: it never sees a
// partial pair, and rolling back is "don't rename" — the live files stay.
func RenewAll(opts RenewOptions) ([]RenewResult, error) {
	certs, err := ListCerts()
	if err != nil {
		return nil, fmt.Errorf("list certs: %w", err)
	}
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = RenewalThreshold
	}

	results := make([]RenewResult, 0, len(certs))
	for _, ci := range certs {
		r := RenewResult{
			Domain:    ci.Domain,
			Strategy:  ci.Strategy,
			OldExpiry: ci.NotAfter,
		}
		needs := opts.Force || time.Until(ci.NotAfter) < threshold

		if !needs {
			r.DaysLeft = ci.DaysLeft
			r.NewExpiry = ci.NotAfter
			results = append(results, r)
			continue
		}

		if opts.DryRun {
			r.Renewed = true
			r.NewExpiry = ci.NotAfter.Add(90 * 24 * time.Hour) // projected
			r.DaysLeft = 90
			results = append(results, r)
			continue
		}

		req := ObtainRequest{
			Strategy:     ci.Strategy,
			Domains:      []string{ci.Domain},
			Email:        opts.Email,
			Staging:      opts.Staging,
			DuckDNSToken: opts.DuckDNSToken,
			CommonName:   ci.Domain, // for self-signed
		}
		if err := Obtain(req, true); err != nil {
			r.Error = err
			results = append(results, r)
			continue
		}
		after, lerr := LoadCertInfo(ci.Domain)
		if lerr == nil {
			r.Renewed = true
			r.NewExpiry = after.NotAfter
			r.DaysLeft = after.DaysLeft
		} else {
			r.Error = lerr
		}
		results = append(results, r)
	}
	return results, nil
}
