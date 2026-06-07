// OAuth 2.0 Token Introspection (RFC 7662) — for opaque bearer tokens
// minted by a confidential IdP (Keycloak admin tokens, Hydra, etc).
// Unlike JWTs, we can't validate them locally; we POST to the IdP's
// introspection endpoint, parse the `active` field, and cache the
// result for CacheSeconds to keep latency reasonable.
//
// Why not stuff this into the JWT verifier: the IdP-roundtrip model is
// distinct enough (network failure → 503, cache key on the token,
// scopes vs. claims) that mixing them up reads worse than a sibling.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth2Config mirrors config.OAuth2.
type OAuth2Config struct {
	IntrospectionURL string
	ClientID         string
	ClientSecret     string
	RequiredScopes   []string
	CacheSeconds     int
}

// IntrospectionResponse is the RFC 7662 shape (subset).
type IntrospectionResponse struct {
	Active   bool   `json:"active"`
	Scope    string `json:"scope,omitempty"`
	Username string `json:"username,omitempty"`
	Subject  string `json:"sub,omitempty"`
	Audience string `json:"aud,omitempty"`
	Issuer   string `json:"iss,omitempty"`
	Expiry   int64  `json:"exp,omitempty"`
}

// OAuth2Verifier holds the introspection cache. One per API.
type OAuth2Verifier struct {
	cfg  OAuth2Config
	http *http.Client

	mu    sync.RWMutex
	cache map[string]cachedIntro
}

type cachedIntro struct {
	resp    IntrospectionResponse
	expires time.Time
}

// NewOAuth2Verifier returns a configured verifier. Errors out on
// missing required fields so the dashboard rejects bad config at
// load time.
func NewOAuth2Verifier(cfg OAuth2Config) (*OAuth2Verifier, error) {
	if cfg.IntrospectionURL == "" {
		return nil, fmt.Errorf("oauth2: introspection_url required")
	}
	if _, err := url.Parse(cfg.IntrospectionURL); err != nil {
		return nil, fmt.Errorf("oauth2: bad introspection_url: %w", err)
	}
	if cfg.CacheSeconds <= 0 {
		cfg.CacheSeconds = 30
	}
	return &OAuth2Verifier{
		cfg:   cfg,
		http:  &http.Client{Timeout: 5 * time.Second},
		cache: map[string]cachedIntro{},
	}, nil
}

// VerifyToken returns OK/Invalid/Backend with the introspection
// response (or nil when we can't reach the IdP).
//
// Cache: per-token TTL = min(CacheSeconds, time-until-exp). A token
// rotated mid-cache-window survives only until the cached entry
// expires; operators who need instant revocation should set
// CacheSeconds: 0 in config (defaults to 30s).
func (v *OAuth2Verifier) VerifyToken(ctx context.Context, token string) (Result, *IntrospectionResponse, string) {
	if token == "" {
		return InvalidToken, nil, "empty token"
	}
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))

	v.mu.RLock()
	if c, ok := v.cache[token]; ok && time.Now().Before(c.expires) {
		resp := c.resp
		v.mu.RUnlock()
		if !resp.Active {
			return InvalidToken, &resp, "cached: inactive"
		}
		if !v.scopesOK(resp.Scope) {
			return MissingClaims, &resp, "missing scope"
		}
		return OK, &resp, "cached ok"
	}
	v.mu.RUnlock()

	body := url.Values{}
	body.Set("token", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.cfg.IntrospectionURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return BackendError, nil, "build request: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if v.cfg.ClientID != "" {
		req.SetBasicAuth(v.cfg.ClientID, v.cfg.ClientSecret)
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return BackendError, nil, "introspect: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BackendError, nil, fmt.Sprintf("introspect status %d", resp.StatusCode)
	}
	var ir IntrospectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return BackendError, nil, "decode: " + err.Error()
	}
	v.cacheEntry(token, ir)
	if !ir.Active {
		return InvalidToken, &ir, "inactive"
	}
	if !v.scopesOK(ir.Scope) {
		return MissingClaims, &ir, "missing required scope"
	}
	return OK, &ir, "ok"
}

func (v *OAuth2Verifier) cacheEntry(token string, ir IntrospectionResponse) {
	ttl := time.Duration(v.cfg.CacheSeconds) * time.Second
	if ir.Expiry > 0 {
		untilExp := time.Until(time.Unix(ir.Expiry, 0))
		if untilExp < ttl && untilExp > 0 {
			ttl = untilExp
		}
	}
	if ttl <= 0 {
		return
	}
	v.mu.Lock()
	v.cache[token] = cachedIntro{resp: ir, expires: time.Now().Add(ttl)}
	// LRU-ish bound: drop a random entry when over 4096.
	if len(v.cache) > 4096 {
		for k := range v.cache {
			delete(v.cache, k)
			break
		}
	}
	v.mu.Unlock()
}

func (v *OAuth2Verifier) scopesOK(claimed string) bool {
	if len(v.cfg.RequiredScopes) == 0 {
		return true
	}
	have := map[string]struct{}{}
	for _, s := range strings.Fields(claimed) {
		have[s] = struct{}{}
	}
	for _, want := range v.cfg.RequiredScopes {
		if _, ok := have[want]; !ok {
			return false
		}
	}
	return true
}
