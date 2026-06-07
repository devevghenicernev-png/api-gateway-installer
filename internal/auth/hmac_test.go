package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"net/http"
	"strings"
	"testing"
	"time"
)

// signFor builds a valid HMACRequest signed with `secret` using `alg`.
func signFor(t *testing.T, secret, keyID, alg string, method, path, query, body string) HMACRequest {
	t.Helper()
	hctor, ok := supportedHMACAlgs[alg]
	if !ok {
		t.Fatalf("unsupported alg in test: %s", alg)
	}
	date := time.Now().UTC().Format(time.RFC3339)
	nonce := "nonce-" + alg + "-" + date
	contentHash := "UNSIGNED-PAYLOAD"
	if body != "" {
		s := sha256.Sum256([]byte(body))
		contentHash = hexBytes(s[:])
	}
	canonical := strings.ToUpper(method) + "\n" + path + "\n" + query + "\n" + date + "\n" + nonce + "\n" + contentHash
	sig := mac(hctor, secret, canonical)
	return HMACRequest{
		Method:        method,
		Path:          path,
		Query:         query,
		Authorization: "HMAC " + keyID + ":" + base64.StdEncoding.EncodeToString(sig),
		Date:          date,
		Nonce:         nonce,
		ContentSHA256: contentHash,
	}
}

func mac(hctor func() hash.Hash, secret, s string) []byte {
	h := hmac.New(hctor, []byte(secret))
	h.Write([]byte(s))
	return h.Sum(nil)
}

func hexBytes(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = hexChars[x>>4]
		out[i*2+1] = hexChars[x&0x0f]
	}
	return string(out)
}

func baseCfg() HMACConfig {
	return HMACConfig{
		Algorithms: []string{"hmac-sha256", "hmac-sha512"},
		ClockSkew:  5 * time.Minute,
		Keys: []HMACKeyEntry{
			{ID: "ci-bot", Secret: "supersecret", Owner: "ci-team", Scopes: []string{"read", "write"}},
		},
	}
}

func TestVerifyHMAC_OK(t *testing.T) {
	cfg := baseCfg()
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "POST", "/api/billing/charge", "ref=42", `{"amount":100}`)
	res, entry, why := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACOK {
		t.Fatalf("want OK, got %d (%s)", res, why)
	}
	if entry == nil || entry.ID != "ci-bot" {
		t.Fatalf("entry not returned")
	}
}

func TestVerifyHMAC_BadSignature(t *testing.T) {
	cfg := baseCfg()
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	// flip one byte
	sig := strings.TrimPrefix(req.Authorization, "HMAC ci-bot:")
	raw, _ := base64.StdEncoding.DecodeString(sig)
	raw[0] ^= 0xff
	req.Authorization = "HMAC ci-bot:" + base64.StdEncoding.EncodeToString(raw)
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACBadSignature {
		t.Fatalf("want HMACBadSignature, got %d", res)
	}
}

func TestVerifyHMAC_MissingAuthorization(t *testing.T) {
	res, _, _ := VerifyHMAC(baseCfg(), HMACRequest{Method: "GET", Path: "/x", Date: "x", Nonce: "x"}, newNonceCache(100))
	if res != HMACMissing {
		t.Fatalf("want HMACMissing, got %d", res)
	}
	if got := res.HTTPStatus(); got != http.StatusUnauthorized {
		t.Fatalf("HTTPStatus = %d, want 401", got)
	}
}

func TestVerifyHMAC_WrongScheme(t *testing.T) {
	req := HMACRequest{Authorization: "Bearer foo", Method: "GET", Path: "/x", Date: "x", Nonce: "x"}
	res, _, _ := VerifyHMAC(baseCfg(), req, newNonceCache(100))
	if res != HMACMissing {
		t.Fatalf("want HMACMissing, got %d", res)
	}
}

func TestVerifyHMAC_MalformedAuth(t *testing.T) {
	cases := []string{
		"HMAC nokey-no-colon",
		"HMAC :empty-id-section",
		"HMAC empty-sig:",
		"HMAC ci-bot:!!!notbase64!!!",
	}
	for _, a := range cases {
		req := HMACRequest{Authorization: a, Method: "GET", Path: "/x", Date: "x", Nonce: "x"}
		res, _, _ := VerifyHMAC(baseCfg(), req, newNonceCache(100))
		if res != HMACMalformed {
			t.Fatalf("Authorization=%q: want HMACMalformed, got %d", a, res)
		}
	}
}

func TestVerifyHMAC_UnknownKey(t *testing.T) {
	cfg := baseCfg()
	req := signFor(t, "supersecret", "no-such-id", "hmac-sha256", "GET", "/x", "", "")
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACUnknownKey {
		t.Fatalf("want HMACUnknownKey, got %d", res)
	}
}

func TestVerifyHMAC_DisabledKey(t *testing.T) {
	cfg := baseCfg()
	cfg.Keys[0].Disabled = true
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACDisabled {
		t.Fatalf("want HMACDisabled, got %d", res)
	}
}

func TestVerifyHMAC_ExpiredKey(t *testing.T) {
	cfg := baseCfg()
	cfg.Keys[0].ExpiresAt = time.Now().Add(-1 * time.Hour)
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACExpired {
		t.Fatalf("want HMACExpired, got %d", res)
	}
}

func TestVerifyHMAC_ClockSkewPositive(t *testing.T) {
	cfg := baseCfg()
	cfg.ClockSkew = 1 * time.Minute
	// Sign with a date 10 minutes in the future.
	future := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	canonical := "GET\n/x\n\n" + future + "\nn1\nUNSIGNED-PAYLOAD"
	sig := mac(sha256.New, "supersecret", canonical)
	req := HMACRequest{
		Method: "GET", Path: "/x",
		Authorization: "HMAC ci-bot:" + base64.StdEncoding.EncodeToString(sig),
		Date:          future, Nonce: "n1",
	}
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACClockSkew {
		t.Fatalf("want HMACClockSkew, got %d", res)
	}
}

func TestVerifyHMAC_ClockSkewNegative(t *testing.T) {
	cfg := baseCfg()
	cfg.ClockSkew = 1 * time.Minute
	past := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	canonical := "GET\n/x\n\n" + past + "\nn1\nUNSIGNED-PAYLOAD"
	sig := mac(sha256.New, "supersecret", canonical)
	req := HMACRequest{
		Method: "GET", Path: "/x",
		Authorization: "HMAC ci-bot:" + base64.StdEncoding.EncodeToString(sig),
		Date:          past, Nonce: "n1",
	}
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACClockSkew {
		t.Fatalf("want HMACClockSkew, got %d", res)
	}
}

func TestVerifyHMAC_MalformedDate(t *testing.T) {
	cfg := baseCfg()
	req := HMACRequest{
		Method: "GET", Path: "/x",
		Authorization: "HMAC ci-bot:" + base64.StdEncoding.EncodeToString([]byte("xx")),
		Date:          "not-a-date", Nonce: "n1",
	}
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACMalformed {
		t.Fatalf("want HMACMalformed, got %d", res)
	}
}

func TestVerifyHMAC_NonceReplay(t *testing.T) {
	cfg := baseCfg()
	cache := newNonceCache(100)
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	if res, _, _ := VerifyHMAC(cfg, req, cache); res != HMACOK {
		t.Fatalf("first call: want OK, got %d", res)
	}
	// Same request again — nonce already in cache.
	if res, _, _ := VerifyHMAC(cfg, req, cache); res != HMACReplay {
		t.Fatalf("second call: want HMACReplay, got %d", res)
	}
}

func TestVerifyHMAC_UnknownAlgorithm(t *testing.T) {
	cfg := baseCfg()
	cfg.Keys[0].Algorithm = "hmac-md5" // not in supportedHMACAlgs
	cfg.Algorithms = []string{"hmac-md5"}
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACUnknownAlg {
		t.Fatalf("want HMACUnknownAlg, got %d", res)
	}
}

func TestVerifyHMAC_AlgorithmNotInAllowList(t *testing.T) {
	cfg := baseCfg()
	cfg.Algorithms = []string{"hmac-sha512"} // only 512 allowed
	cfg.Keys[0].Algorithm = "hmac-sha256"    // key wants 256
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "GET", "/x", "", "")
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACUnknownAlg {
		t.Fatalf("want HMACUnknownAlg, got %d", res)
	}
}

func TestVerifyHMAC_SHA512(t *testing.T) {
	cfg := baseCfg()
	cfg.Keys[0].Algorithm = "hmac-sha512"
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha512", "POST", "/x", "", "body")
	res, _, why := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACOK {
		t.Fatalf("want OK, got %d (%s)", res, why)
	}
	_ = sha512.New // keep import alive in test
}

func TestVerifyHMAC_RequireBodyHash(t *testing.T) {
	cfg := baseCfg()
	cfg.RequireBodyHash = true
	// Build a request with UNSIGNED-PAYLOAD body hash.
	date := time.Now().UTC().Format(time.RFC3339)
	canonical := "POST\n/x\n\n" + date + "\nn1\nUNSIGNED-PAYLOAD"
	sig := mac(sha256.New, "supersecret", canonical)
	req := HMACRequest{
		Method: "POST", Path: "/x",
		Authorization: "HMAC ci-bot:" + base64.StdEncoding.EncodeToString(sig),
		Date:          date, Nonce: "n1",
		ContentSHA256: "UNSIGNED-PAYLOAD",
	}
	res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100))
	if res != HMACMissingBodyHash {
		t.Fatalf("want HMACMissingBodyHash, got %d", res)
	}
}

func TestVerifyHMAC_QueryAndMethodInSignature(t *testing.T) {
	cfg := baseCfg()
	req := signFor(t, "supersecret", "ci-bot", "hmac-sha256", "DELETE", "/items/42", "soft=true", "")
	if res, _, _ := VerifyHMAC(cfg, req, newNonceCache(100)); res != HMACOK {
		t.Fatalf("baseline OK failed")
	}
	// Mutate path — signature must reject.
	req2 := req
	req2.Nonce = req.Nonce + "x"
	req2.Path = "/items/43"
	if res, _, _ := VerifyHMAC(cfg, req2, newNonceCache(100)); res != HMACBadSignature {
		t.Fatalf("path tamper: want HMACBadSignature, got %d", res)
	}
	// Mutate query — same.
	req3 := req
	req3.Nonce = req.Nonce + "y"
	req3.Query = "soft=false"
	if res, _, _ := VerifyHMAC(cfg, req3, newNonceCache(100)); res != HMACBadSignature {
		t.Fatalf("query tamper: want HMACBadSignature, got %d", res)
	}
	// Mutate method — same.
	req4 := req
	req4.Nonce = req.Nonce + "z"
	req4.Method = "PUT"
	if res, _, _ := VerifyHMAC(cfg, req4, newNonceCache(100)); res != HMACBadSignature {
		t.Fatalf("method tamper: want HMACBadSignature, got %d", res)
	}
}

func TestVerifyHMAC_HTTPStatusMapping(t *testing.T) {
	cases := []struct {
		r    HMACResult
		want int
	}{
		{HMACOK, 200},
		{HMACMissing, 401},
		{HMACMalformed, 401},
		{HMACUnknownAlg, 401},
		{HMACUnknownKey, 401},
		{HMACBadSignature, 401},
		{HMACClockSkew, 403},
		{HMACReplay, 403},
		{HMACExpired, 403},
		{HMACDisabled, 403},
		{HMACMissingBodyHash, 403},
	}
	for _, c := range cases {
		if got := c.r.HTTPStatus(); got != c.want {
			t.Errorf("%v: got %d, want %d", c.r, got, c.want)
		}
	}
}
