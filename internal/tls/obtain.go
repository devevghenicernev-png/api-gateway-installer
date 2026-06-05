package tls

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/duckdns"
	"github.com/go-acme/lego/v4/registration"
)

// Obtain runs the ACME flow for `req` and writes the resulting cert under
// /var/lib/apigw/certs/<domain>/.
//
// Idempotent across runs: the account key + registration are reused; if a
// cert for the same domain already exists and isn't near expiry, the call
// short-circuits unless `force` is true.
//
// `force` is set by `apigw tls renew --force` and by the `apigw tls
// add-domain` path (where you do want a new SAN list).
func Obtain(req ObtainRequest, force bool) error {
	if err := validate(req); err != nil {
		return err
	}

	// Self-signed bypasses ACME entirely.
	if req.Strategy == StrategySelfSigned {
		return obtainSelfSigned(req)
	}

	// Skip if everything's already valid and not near expiry.
	if !force {
		stale := false
		for _, d := range req.Domains {
			ci, err := LoadCertInfo(d)
			if err != nil || time.Until(ci.NotAfter) < RenewalThreshold {
				stale = true
				break
			}
		}
		if !stale {
			return nil
		}
	}

	user, err := LoadOrCreateAccount(AccountDir, req.Email)
	if err != nil {
		return fmt.Errorf("acme account: %w", err)
	}

	cfg := lego.NewConfig(user)
	if req.Staging {
		cfg.CADirURL = lego.LEDirectoryStaging
	} else {
		cfg.CADirURL = lego.LEDirectoryProduction
	}
	cfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("lego client: %w", err)
	}

	// Choose solver based on strategy.
	switch req.Strategy {
	case StrategyLetsEncrypt:
		// Webroot, NOT standalone. nginx stays up on :80 and serves the
		// challenge file out of /var/lib/apigw/acme-webroot/ — we just drop
		// the token file there. See webroot.go and the tls-server.tmpl
		// `/.well-known/acme-challenge/` location.
		wp, err := NewWebrootProvider(AcmeWebrootDir)
		if err != nil {
			return fmt.Errorf("webroot provider: %w", err)
		}
		if err := client.Challenge.SetHTTP01Provider(wp); err != nil {
			return fmt.Errorf("http-01 provider: %w", err)
		}

	case StrategyDuckDNS:
		p, err := duckdns.NewDNSProviderConfig(&duckdns.Config{
			Token:              req.DuckDNSToken,
			PropagationTimeout: 120 * time.Second, // ARCHITECTURE.md §"DuckDNS propagation"
			PollingInterval:    5 * time.Second,
			SequenceInterval:   60 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("duckdns provider: %w", err)
		}
		if err := client.Challenge.SetDNS01Provider(p); err != nil {
			return fmt.Errorf("dns-01 provider: %w", err)
		}

	default:
		return fmt.Errorf("unsupported acme strategy: %s", req.Strategy)
	}

	// Register the account if we haven't already.
	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return fmt.Errorf("acme register: %w", err)
		}
		user.Registration = reg
		if err := user.Save(AccountDir); err != nil {
			return fmt.Errorf("save account: %w", err)
		}
	}

	// Obtain — one cert covers all SANs in req.Domains.
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Domains,
		Bundle:  true,
	})
	if err != nil {
		return fmt.Errorf("obtain: %w", err)
	}
	if len(res.Certificate) == 0 || len(res.PrivateKey) == 0 {
		return errors.New("obtain returned empty cert/key")
	}

	// Write the same bundle under each domain dir — symlinks would be tidier
	// but cause SELinux/nginx issues on some distros. Disk is cheap.
	for _, d := range req.Domains {
		if err := StoreCert(d, res.Certificate, res.PrivateKey, req.Strategy); err != nil {
			return fmt.Errorf("store cert for %s: %w", d, err)
		}
	}
	return nil
}

func validate(req ObtainRequest) error {
	if !req.Strategy.IsValid() || req.Strategy == StrategyNone {
		return fmt.Errorf("invalid tls strategy %q", req.Strategy)
	}
	if req.Strategy == StrategySelfSigned {
		if req.CommonName == "" {
			return errors.New("self-signed requires --cn")
		}
		return nil
	}
	if req.Email == "" {
		return errors.New("acme requires --email")
	}
	if len(req.Domains) == 0 {
		return errors.New("acme requires at least one domain")
	}
	for _, d := range req.Domains {
		if strings.TrimSpace(d) == "" {
			return errors.New("domain is empty")
		}
	}
	if req.Strategy == StrategyDuckDNS && req.DuckDNSToken == "" {
		return errors.New("duckdns requires --token")
	}
	// Wildcard requires DNS-01.
	for _, d := range req.Domains {
		if strings.HasPrefix(d, "*.") && req.Strategy != StrategyDuckDNS {
			return fmt.Errorf("wildcard domain %q requires the duckdns/dns-01 strategy", d)
		}
	}
	return nil
}
