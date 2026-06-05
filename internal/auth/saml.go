// Package auth — SAML 2.0 SSO via crewjam/saml.
//
// Flow:
//
//  1. Operator configures SAML in apigw config: IdPMetadataURL or
//     IdPMetadataFile (xml), AppCertFile (apigw's SP cert), AppKeyFile.
//
//  2. Dashboard daemon exposes /saml/metadata (SP metadata XML), /saml/acs
//     (assertion consumer), /saml/sls (single-logout).
//
//  3. Browser hits /login → redirect to IdP → assertion posted to /saml/acs
//     → apigw extracts NameID + group attributes → session JWT issued →
//     cookie set.
//
//  4. CLI uses `apigw login --saml` — opens browser to IdP, IdP redirects
//     back to a localhost listener apigw spins up, captures the session
//     token, writes it to ~/.config/apigw/session.
//
// This file wires the SP and exposes the http handlers; the dashboard
// server registers them at startup.
package auth

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
)

// SAMLConfig holds the SP-side wiring.
type SAMLConfig struct {
	IdPMetadataURL  string // URL of the IdP metadata; one of URL/File required
	IdPMetadataFile string // local path alternative

	AppRootURL string // public URL of apigw, e.g. https://apigw.example.com

	SPCertFile string // apigw's SP cert (X.509 PEM)
	SPKeyFile  string // apigw's SP private key (RSA PEM)

	// GroupAttribute names the SAML attribute carrying group memberships.
	// Common values: "memberOf" (AD), "groups" (Okta).
	GroupAttribute string

	// UseStaging skips signing the AuthnRequest — only for dev.
	UseStaging bool
}

// NewSAMLMiddleware builds the SP middleware. The dashboard server mounts
// it at /saml/ to handle metadata, ACS, and SLS.
func NewSAMLMiddleware(cfg SAMLConfig) (*samlsp.Middleware, error) {
	if cfg.AppRootURL == "" {
		return nil, errors.New("saml: AppRootURL required")
	}
	root, err := url.Parse(cfg.AppRootURL)
	if err != nil {
		return nil, fmt.Errorf("saml: parse AppRootURL: %w", err)
	}

	cert, key, err := loadSPKeypair(cfg.SPCertFile, cfg.SPKeyFile)
	if err != nil {
		return nil, err
	}

	idpMetadata, err := loadIdPMetadata(cfg)
	if err != nil {
		return nil, err
	}

	mw, err := samlsp.New(samlsp.Options{
		URL:               *root,
		Key:               key,
		Certificate:       cert,
		IDPMetadata:       idpMetadata,
		AllowIDPInitiated: true,
	})
	if err != nil {
		return nil, fmt.Errorf("saml: new middleware: %w", err)
	}
	return mw, nil
}

func loadSPKeypair(certFile, keyFile string) (*x509.Certificate, *rsa.PrivateKey, error) {
	if certFile == "" || keyFile == "" {
		return nil, nil, errors.New("saml: SPCertFile and SPKeyFile required")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("saml load keypair: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("saml parse cert: %w", err)
	}
	rsaKey, ok := pair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("saml: key must be RSA (ECDSA not supported by crewjam/saml)")
	}
	return cert, rsaKey, nil
}

func loadIdPMetadata(cfg SAMLConfig) (*saml.EntityDescriptor, error) {
	var body []byte
	if cfg.IdPMetadataFile != "" {
		b, err := os.ReadFile(cfg.IdPMetadataFile)
		if err != nil {
			return nil, fmt.Errorf("saml read idp metadata: %w", err)
		}
		body = b
	} else if cfg.IdPMetadataURL != "" {
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Get(cfg.IdPMetadataURL)
		if err != nil {
			return nil, fmt.Errorf("saml fetch idp metadata: %w", err)
		}
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("saml: IdPMetadataURL or IdPMetadataFile required")
	}

	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(body, &ed); err != nil {
		return nil, fmt.Errorf("saml unmarshal idp metadata: %w", err)
	}
	return &ed, nil
}

// SubjectFromSession extracts the authenticated user's NameID + groups
// from the SAML session attached to the request. Returns ("", nil) if
// no session is present (handler not protected, or unauth'd request).
func SubjectFromSession(r *http.Request, groupAttr string) (string, []string) {
	sa, err := samlsp.SessionFromContext(r.Context()).(samlsp.SessionWithAttributes)
	if err == false {
		// SessionFromContext returns nil interface when no session; assertion
		// fails. Just return zero values.
		_ = sa
		return "", nil
	}
	if sa == nil {
		return "", nil
	}
	attrs := sa.GetAttributes()
	nameID := attrs.Get("nameid")
	groups := attrs[groupAttr]
	return nameID, groups
}
