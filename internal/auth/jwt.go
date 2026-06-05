// Package auth provides JWT token verification used by the dashboard daemon
// to serve nginx's `auth_request` queries.
//
// Flow: nginx receives a request → location block has `auth_request
// /_apigw_jwt/<api>`. Nginx makes a sub-request to the dashboard daemon's
// /auth/jwt/<api> endpoint, forwarding the original Authorization header.
// The dashboard parses+verifies the JWT against the API's configured key
// material (HMAC secret or JWKS URL) and replies 200/401/403. Nginx then
// allows or rejects the original request based on that status.
//
// Why not nginx's auth_jwt module? It's commercial-only (NGINX Plus). The
// OSS auth_request + our small Go validator gives us the same effect with
// zero added moving parts — we already run the dashboard daemon.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// JWTConfig describes per-API JWT validation rules. One of HMACSecret or
// JWKSURL must be set. RequireClaims is a map of claim name → expected
// string value (e.g. {"role": "admin"}).
type JWTConfig struct {
	// Algorithm — HS256, HS384, HS512, RS256, RS384, RS512, ES256, ES384,
	// ES512, EdDSA. Required: we never auto-detect from the token (that
	// would let an attacker downgrade RS256 → HS256 with a chosen key).
	Algorithm string

	// HMACSecret is the shared secret for HS* algorithms. Required iff
	// Algorithm starts with "HS".
	HMACSecret string

	// JWKSURL is the URL of a JWKS endpoint (e.g. Auth0, Keycloak, Okta).
	// We fetch on first use and refresh every JWKSRefreshInterval.
	JWKSURL string

	// JWKSRefreshInterval defaults to 1 hour. Operators can shorten it if
	// their provider rotates keys aggressively.
	JWKSRefreshInterval time.Duration

	// Issuer, when set, must match the token's `iss` claim exactly.
	Issuer string

	// Audience, when set, must match the token's `aud` claim. The claim
	// may be a string or a JSON array — we accept either form.
	Audience string

	// RequireClaims maps claim names to expected string values. All
	// listed claims must be present and equal; missing or mismatched
	// claims fail with 403.
	RequireClaims map[string]string
}

// Verifier holds per-API state: the parsed key + cached JWKS.
type Verifier struct {
	cfg JWTConfig

	mu          sync.RWMutex
	jwks        *jose.JSONWebKeySet
	jwksFetched time.Time

	// http is overridable in tests.
	http *http.Client
}

// NewVerifier builds a Verifier for the given config. Errors out early on
// invalid config (e.g. HS256 with no secret).
func NewVerifier(cfg JWTConfig) (*Verifier, error) {
	if cfg.Algorithm == "" {
		return nil, errors.New("jwt: Algorithm is required")
	}
	if strings.HasPrefix(cfg.Algorithm, "HS") && cfg.HMACSecret == "" {
		return nil, fmt.Errorf("jwt: %s requires HMACSecret", cfg.Algorithm)
	}
	if !strings.HasPrefix(cfg.Algorithm, "HS") && cfg.JWKSURL == "" {
		return nil, fmt.Errorf("jwt: %s requires JWKSURL", cfg.Algorithm)
	}
	if cfg.JWKSRefreshInterval == 0 {
		cfg.JWKSRefreshInterval = time.Hour
	}
	return &Verifier{
		cfg: cfg,
		http: &http.Client{
			Timeout: 5 * time.Second,
			// Cap redirects at 2 — a misbehaving or malicious IdP can otherwise
			// chain up to 10 hops (stdlib default) and stall auth_request calls.
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 2 {
					return errors.New("jwks: too many redirects")
				}
				return nil
			},
		},
	}, nil
}

// Result categorizes the outcome of a Verify call. http_status maps:
//
//	OK              → 200 (auth_request passes)
//	InvalidToken    → 401 (signature/expiry/issuer fail)
//	MissingClaims   → 403 (signature OK but RequireClaims unsatisfied)
//	BackendError    → 500 (JWKS fetch failed, etc.)
type Result int

const (
	OK Result = iota
	InvalidToken
	MissingClaims
	BackendError
)

// HTTPStatus maps a Result to the response code we send back to nginx.
func (r Result) HTTPStatus() int {
	switch r {
	case OK:
		return http.StatusOK
	case InvalidToken:
		return http.StatusUnauthorized
	case MissingClaims:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// Verify parses and validates `token`. Returns the Result + parsed claims +
// human-readable reason. The reason is logged but never sent back to nginx
// (auth_request returns status only) — operators read it in journald.
func (v *Verifier) Verify(ctx context.Context, token string) (Result, map[string]any, string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return InvalidToken, nil, "empty token"
	}
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")

	algoSet, err := signatureAlgorithms(v.cfg.Algorithm)
	if err != nil {
		return BackendError, nil, err.Error()
	}
	sig, err := jose.ParseSigned(token, algoSet)
	if err != nil {
		return InvalidToken, nil, "parse: " + err.Error()
	}

	key, err := v.resolveKey(ctx, sig)
	if err != nil {
		return BackendError, nil, "resolve key: " + err.Error()
	}

	payload, err := sig.Verify(key)
	if err != nil {
		return InvalidToken, nil, "verify: " + err.Error()
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return InvalidToken, nil, "decode claims: " + err.Error()
	}

	// Standard claims: exp + nbf + iat.
	now := time.Now().Unix()
	if exp, ok := numericClaim(claims, "exp"); ok && exp < now {
		return InvalidToken, claims, "token expired"
	}
	if nbf, ok := numericClaim(claims, "nbf"); ok && nbf > now {
		return InvalidToken, claims, "token not yet valid"
	}

	if v.cfg.Issuer != "" {
		if iss, _ := claims["iss"].(string); iss != v.cfg.Issuer {
			return InvalidToken, claims, fmt.Sprintf("iss mismatch: got %q want %q", iss, v.cfg.Issuer)
		}
	}
	if v.cfg.Audience != "" {
		if !audMatches(claims["aud"], v.cfg.Audience) {
			return InvalidToken, claims, "aud mismatch"
		}
	}

	for k, want := range v.cfg.RequireClaims {
		got, _ := claims[k].(string)
		if got != want {
			return MissingClaims, claims, fmt.Sprintf("claim %q: got %q want %q", k, got, want)
		}
	}

	return OK, claims, "ok"
}

// resolveKey returns the key matching `sig`'s kid. For HMAC modes the
// secret bytes are used directly. For asymmetric algos we fetch+cache the
// JWKS and pick by kid.
func (v *Verifier) resolveKey(ctx context.Context, sig *jose.JSONWebSignature) (any, error) {
	if strings.HasPrefix(v.cfg.Algorithm, "HS") {
		return []byte(v.cfg.HMACSecret), nil
	}
	keyID := ""
	if len(sig.Signatures) > 0 {
		keyID = sig.Signatures[0].Header.KeyID
	}
	jwks, err := v.getJWKS(ctx)
	if err != nil {
		return nil, err
	}
	if keyID != "" {
		ks := jwks.Key(keyID)
		if len(ks) > 0 {
			return ks[0].Key, nil
		}
	}
	// No kid match — try the first key. Many test JWKS endpoints publish
	// one key without a kid.
	if len(jwks.Keys) > 0 {
		return jwks.Keys[0].Key, nil
	}
	return nil, errors.New("no matching key in JWKS")
}

// getJWKS returns the cached JWKS or fetches a fresh one if expired.
func (v *Verifier) getJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	v.mu.RLock()
	if v.jwks != nil && time.Since(v.jwksFetched) < v.cfg.JWKSRefreshInterval {
		js := v.jwks
		v.mu.RUnlock()
		return js, nil
	}
	v.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	var js jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&js); err != nil {
		return nil, fmt.Errorf("jwks decode: %w", err)
	}

	v.mu.Lock()
	v.jwks = &js
	v.jwksFetched = time.Now()
	v.mu.Unlock()
	return &js, nil
}

// signatureAlgorithms maps our config's Algorithm string to the
// jose.SignatureAlgorithm allow-list. Single-element list — never accept
// "any algorithm" since that's the classic JWT alg-confusion attack.
func signatureAlgorithms(algo string) ([]jose.SignatureAlgorithm, error) {
	a := jose.SignatureAlgorithm(algo)
	switch a {
	case jose.HS256, jose.HS384, jose.HS512,
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
		jose.PS256, jose.PS384, jose.PS512,
		jose.EdDSA:
		return []jose.SignatureAlgorithm{a}, nil
	}
	return nil, fmt.Errorf("unsupported algorithm %q", algo)
}

// numericClaim returns a numeric claim as int64, tolerating float64
// (JSON's default for numbers) and int.
func numericClaim(claims map[string]any, key string) (int64, bool) {
	v, ok := claims[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// audMatches accepts the standard "aud claim is string OR string array"
// quirk from RFC 7519.
func audMatches(claim any, want string) bool {
	switch v := claim.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, _ := item.(string); s == want {
				return true
			}
		}
	}
	return false
}

// keepAlive prevents the rsa import from being optimized out if a build
// later trims it; keeping the symbol references the package guard.
var _ = (*rsa.PublicKey)(nil)
