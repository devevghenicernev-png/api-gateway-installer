// HMAC request-signing verification. Clients send
// `Authorization: HMAC <key-id>:<base64-sig>` plus `X-Apigw-Date`,
// `X-Apigw-Nonce` and (optionally) `X-Apigw-Content-SHA256`. The
// signature covers `<METHOD>\n<PATH>\n<QUERY>\n<DATE>\n<NONCE>\n<CSHA>`,
// so a MITM can't tamper without the shared secret — that's why the
// nginx auth_request subrequest does NOT read the body (fast path).
// Replay is prevented by a per-API nonce LRU.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"hash"
	"net/http"
	"strings"
	"sync"
	"time"
)

type HMACConfig struct {
	Algorithms      []string
	ClockSkew       time.Duration
	RequireBodyHash bool
	NonceCacheSize  int
	Keys            []HMACKeyEntry
}

type HMACKeyEntry struct {
	ID        string
	Secret    string
	Algorithm string
	Owner     string
	ExpiresAt time.Time
	Disabled  bool
	Scopes    []string
}

// HMACRequest is the minimum data the verifier needs. The dashboard
// auth-handler fills this from `X-Original-Method` + `X-Original-URI`
// passed by nginx; tests fill it directly.
type HMACRequest struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	Date          string
	Nonce         string
	ContentSHA256 string
}

type HMACResult int

const (
	HMACOK HMACResult = iota
	HMACMissing
	HMACMalformed
	HMACUnknownAlg
	HMACUnknownKey
	HMACBadSignature
	HMACClockSkew
	HMACReplay
	HMACExpired
	HMACDisabled
	HMACMissingBodyHash
)

func (r HMACResult) HTTPStatus() int {
	switch r {
	case HMACOK:
		return http.StatusOK
	case HMACMissing, HMACMalformed, HMACBadSignature, HMACUnknownKey, HMACUnknownAlg:
		return http.StatusUnauthorized
	case HMACClockSkew, HMACReplay, HMACExpired, HMACDisabled, HMACMissingBodyHash:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// supportedHMACAlgs maps the wire algorithm name to the hash constructor.
var supportedHMACAlgs = map[string]func() hash.Hash{
	"hmac-sha256": sha256.New,
	"hmac-sha512": sha512.New,
}

// nonceCache is a tiny time-bounded set. Per-API instance — replays
// from one API can't be reused against another (different key sets +
// different nonce buckets).
type nonceCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	cap     int
}

func newNonceCache(capacity int) *nonceCache {
	if capacity <= 0 {
		capacity = 10000
	}
	return &nonceCache{entries: make(map[string]time.Time, capacity), cap: capacity}
}

// seen reports whether the nonce was already accepted within `window`.
// When the cache fills, expired entries are GC'd opportunistically.
func (c *nonceCache) seen(nonce string, window time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.entries) >= c.cap {
		for k, t := range c.entries {
			if now.Sub(t) > window {
				delete(c.entries, k)
			}
		}
	}
	if t, ok := c.entries[nonce]; ok && now.Sub(t) <= window {
		return true
	}
	c.entries[nonce] = now
	return false
}

// HMACNonces holds one nonceCache per API name. The dashboard server
// keeps a single instance; first request for an API allocates lazily.
type HMACNonces struct {
	mu sync.Mutex
	m  map[string]*nonceCache
}

func NewHMACNonces() *HMACNonces {
	return &HMACNonces{m: make(map[string]*nonceCache)}
}

// For returns (allocating if necessary) the per-API nonce cache.
func (h *HMACNonces) For(api string, capacity int) *nonceCache {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.m[api]
	if !ok {
		c = newNonceCache(capacity)
		h.m[api] = c
	}
	return c
}

// VerifyHMAC walks the protocol: parse Authorization, find the key by
// ID, build the canonical signing string, compute the expected
// signature, constant-time-compare, then (only on signature match)
// record the nonce. Returning the matched entry lets the caller surface
// owner + scopes via X-Apigw-* response headers for the upstream.
func VerifyHMAC(cfg HMACConfig, req HMACRequest, nonces *nonceCache) (HMACResult, *HMACKeyEntry, string) {
	algs := cfg.Algorithms
	if len(algs) == 0 {
		algs = []string{"hmac-sha256", "hmac-sha512"}
	}
	skew := cfg.ClockSkew
	if skew <= 0 {
		skew = 5 * time.Minute
	}

	if req.Authorization == "" {
		return HMACMissing, nil, "missing Authorization"
	}
	if !strings.HasPrefix(req.Authorization, "HMAC ") {
		return HMACMissing, nil, "Authorization scheme not HMAC"
	}
	rest := strings.TrimPrefix(req.Authorization, "HMAC ")
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 || colon == len(rest)-1 {
		return HMACMalformed, nil, "Authorization not <key-id>:<sig>"
	}
	keyID, sigB64 := rest[:colon], rest[colon+1:]
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		sig, err = base64.RawStdEncoding.DecodeString(sigB64)
		if err != nil {
			return HMACMalformed, nil, "signature not base64"
		}
	}

	if req.Date == "" || req.Nonce == "" {
		return HMACMalformed, nil, "missing X-Apigw-Date or X-Apigw-Nonce"
	}
	t, err := time.Parse(time.RFC3339, req.Date)
	if err != nil {
		return HMACMalformed, nil, "X-Apigw-Date not RFC3339"
	}
	drift := time.Since(t)
	if drift < 0 {
		drift = -drift
	}
	if drift > skew {
		return HMACClockSkew, nil, "date outside clock-skew window"
	}

	contentHash := req.ContentSHA256
	if cfg.RequireBodyHash && (contentHash == "" || contentHash == "UNSIGNED-PAYLOAD") {
		return HMACMissingBodyHash, nil, "X-Apigw-Content-SHA256 required"
	}
	if contentHash == "" {
		contentHash = "UNSIGNED-PAYLOAD"
	}

	var entry *HMACKeyEntry
	for i := range cfg.Keys {
		if cfg.Keys[i].ID == keyID {
			entry = &cfg.Keys[i]
			break
		}
	}
	if entry == nil {
		return HMACUnknownKey, nil, "key id not found"
	}
	if entry.Disabled {
		return HMACDisabled, entry, "key disabled"
	}
	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt) {
		return HMACExpired, entry, "key expired"
	}

	alg := entry.Algorithm
	if alg == "" {
		alg = algs[0]
	}
	allowed := false
	for _, a := range algs {
		if a == alg {
			allowed = true
			break
		}
	}
	if !allowed {
		return HMACUnknownAlg, entry, "algorithm " + alg + " not in allow-list"
	}
	hctor, ok := supportedHMACAlgs[alg]
	if !ok {
		return HMACUnknownAlg, entry, "unsupported algorithm " + alg
	}

	canonical := strings.ToUpper(req.Method) + "\n" +
		req.Path + "\n" +
		req.Query + "\n" +
		req.Date + "\n" +
		req.Nonce + "\n" +
		contentHash
	mac := hmac.New(hctor, []byte(entry.Secret))
	mac.Write([]byte(canonical))
	expected := mac.Sum(nil)

	if subtle.ConstantTimeCompare(expected, sig) != 1 {
		return HMACBadSignature, entry, "signature mismatch"
	}

	// Replay check last — bogus signatures must not pollute the cache.
	if nonces != nil && nonces.seen(req.Nonce, 2*skew) {
		return HMACReplay, entry, "nonce replayed"
	}

	return HMACOK, entry, "ok"
}
