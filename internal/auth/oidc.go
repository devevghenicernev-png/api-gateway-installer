// OIDC Discovery — fetch /.well-known/openid-configuration from an IdP
// and use it to fill in the JWT verifier's JWKSURL / Issuer fields.
// Lets operators set just `oidc_issuer: https://accounts.example.com`
// instead of plumbing the JWKS URL + issuer + algorithm by hand.
//
// The discovery doc is cached for an hour (typical IdP rotation cadence
// is days/weeks). Fetch failures fall back to the cached values; a
// startup-time fetch of an unreachable IdP returns a configuration
// error so the dashboard never silently runs without auth.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OIDCDiscovery exposes the subset of the discovery document we need.
// authorization_endpoint + token_endpoint are filled when present —
// JWT-only setups skip them, but interactive login flows (admin SSO)
// need them to know where to redirect / exchange codes.
type OIDCDiscovery struct {
	Issuer           string   `json:"issuer"`
	JWKSURI          string   `json:"jwks_uri"`
	AuthorizationURL string   `json:"authorization_endpoint"`
	TokenURL         string   `json:"token_endpoint"`
	Algos            []string `json:"id_token_signing_alg_values_supported"`
}

// OIDCFetcher caches discovery docs per issuer URL.
type OIDCFetcher struct {
	mu    sync.RWMutex
	cache map[string]oidcCached
	http  *http.Client
}

type oidcCached struct {
	doc     OIDCDiscovery
	expires time.Time
}

// NewOIDCFetcher returns a fetcher with a 1-hour cache and conservative
// HTTP timeouts.
func NewOIDCFetcher() *OIDCFetcher {
	return &OIDCFetcher{
		cache: map[string]oidcCached{},
		http: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 2 {
					return errors.New("oidc: too many redirects")
				}
				return nil
			},
		},
	}
}

// Fetch returns the discovery doc for `issuerURL`. Appends the
// well-known suffix if the operator gave the bare issuer URL.
func (f *OIDCFetcher) Fetch(ctx context.Context, issuerURL string) (OIDCDiscovery, error) {
	url := strings.TrimRight(issuerURL, "/")
	if !strings.HasSuffix(url, "/.well-known/openid-configuration") {
		url += "/.well-known/openid-configuration"
	}
	f.mu.RLock()
	if c, ok := f.cache[url]; ok && time.Now().Before(c.expires) {
		f.mu.RUnlock()
		return c.doc, nil
	}
	f.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return OIDCDiscovery{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return OIDCDiscovery{}, fmt.Errorf("oidc fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return OIDCDiscovery{}, fmt.Errorf("oidc fetch: status %d", resp.StatusCode)
	}
	var doc OIDCDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return OIDCDiscovery{}, fmt.Errorf("oidc decode: %w", err)
	}
	if doc.JWKSURI == "" {
		return OIDCDiscovery{}, errors.New("oidc: discovery doc missing jwks_uri")
	}
	f.mu.Lock()
	f.cache[url] = oidcCached{doc: doc, expires: time.Now().Add(time.Hour)}
	f.mu.Unlock()
	return doc, nil
}

// MergeIntoJWTConfig fills missing JWT verifier fields from a discovery
// doc. Existing operator-set values win — discovery is meant to be a
// default-supplier, not an override.
func (d OIDCDiscovery) MergeIntoJWTConfig(j *JWTConfig) {
	if j.JWKSURL == "" {
		j.JWKSURL = d.JWKSURI
	}
	if j.Issuer == "" {
		j.Issuer = d.Issuer
	}
	if j.Algorithm == "" {
		for _, a := range d.Algos {
			if a == "RS256" || a == "ES256" || a == "EdDSA" {
				j.Algorithm = a
				break
			}
		}
	}
}
