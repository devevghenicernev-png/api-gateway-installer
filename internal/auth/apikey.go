// API key verification — the simplest auth model and the one a lot of
// SaaS APIs ship with. nginx's auth_request calls /auth/apikey/<api>
// on every request; we look up the key in the API's APIKeyAuth config
// and reject expired/disabled/missing keys with 401/403.
//
// Keys are stored verbatim in config.yaml today. config.yaml is 0o600
// on disk (the file flock + atomic save in internal/config enforces
// that), so the in-memory and on-disk views match. APIKeyAuth has a
// HashedAtRest field reserved for a future migration to argon2id when
// operators want defence-in-depth against a compromised dashboard.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// APIKeyConfig is the in-memory view of config.APIKeyAuth, kept here so
// internal/auth doesn't import internal/config (which already imports
// internal/auth for JWT — would create a cycle).
type APIKeyConfig struct {
	Header     string
	QueryParam string
	Keys       []APIKeyEntry
}

// APIKeyEntry is the runtime form of one key. Mirrors config.APIKey.
type APIKeyEntry struct {
	ID        string
	Secret    string
	Owner     string
	RPS       int
	ExpiresAt time.Time
	Disabled  bool
	Scopes    []string
}

// APIKeyResult mirrors the JWT verifier's Result enum so the dashboard
// handler can return the same {200,401,403,500} mapping.
type APIKeyResult int

const (
	APIKeyOK APIKeyResult = iota
	APIKeyMissing
	APIKeyInvalid
	APIKeyExpired
	APIKeyDisabled
	APIKeyMissingScope
)

// HTTPStatus maps a result to the nginx auth_request response code.
func (r APIKeyResult) HTTPStatus() int {
	switch r {
	case APIKeyOK:
		return http.StatusOK
	case APIKeyMissing, APIKeyInvalid:
		return http.StatusUnauthorized
	case APIKeyExpired, APIKeyDisabled, APIKeyMissingScope:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// VerifyAPIKey extracts the key from headers/query and matches it
// against the configured set. Returns the matched entry so the caller
// can surface owner + scopes via X-Apigw-* response headers — nginx
// auth_request_set lets the upstream see who's calling.
//
// Comparison is constant-time so an attacker can't enumerate keys via
// timing differences.
func VerifyAPIKey(cfg APIKeyConfig, headers http.Header, query map[string][]string) (APIKeyResult, *APIKeyEntry, string) {
	header := cfg.Header
	if header == "" {
		header = "X-API-Key"
	}
	given := strings.TrimSpace(headers.Get(header))
	if given == "" && cfg.QueryParam != "" {
		if vals, ok := query[cfg.QueryParam]; ok && len(vals) > 0 {
			given = strings.TrimSpace(vals[0])
		}
	}
	// Allow `Authorization: Bearer <key>` as a fallback for clients
	// that can't set arbitrary headers (HTML forms, S3 SDKs).
	if given == "" {
		if h := headers.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			given = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if given == "" {
		return APIKeyMissing, nil, "no api key header/query/Bearer"
	}
	for i := range cfg.Keys {
		k := &cfg.Keys[i]
		if k.Disabled {
			continue
		}
		// constant-time compare — never branch on key bytes.
		if subtle.ConstantTimeCompare([]byte(k.Secret), []byte(given)) != 1 {
			continue
		}
		if !k.ExpiresAt.IsZero() && time.Now().After(k.ExpiresAt) {
			return APIKeyExpired, k, "key " + k.ID + " expired"
		}
		return APIKeyOK, k, "ok"
	}
	return APIKeyInvalid, nil, "no matching key"
}
