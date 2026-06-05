package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// sign produces a JWT signed with the given algo + key.
func sign(t *testing.T, algo jose.SignatureAlgorithm, key any, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algo, Key: key}, &jose.SignerOptions{})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	s, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("CompactSerialize: %v", err)
	}
	return s
}

func TestVerify_HMAC_OK(t *testing.T) {
	v, err := NewVerifier(JWTConfig{
		Algorithm:  "HS256",
		HMACSecret: "supersecret-must-be-at-least-32-bytes-long",
		Issuer:     "test-issuer",
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	token := sign(t, jose.HS256, []byte("supersecret-must-be-at-least-32-bytes-long"), map[string]any{
		"iss": "test-issuer",
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	res, _, why := v.Verify(context.Background(), "Bearer "+token)
	if res != OK {
		t.Fatalf("expected OK; got %v (%s)", res, why)
	}
}

func TestVerify_Expired(t *testing.T) {
	v, _ := NewVerifier(JWTConfig{Algorithm: "HS256", HMACSecret: "supersecret-must-be-at-least-32-bytes-long"})
	token := sign(t, jose.HS256, []byte("supersecret-must-be-at-least-32-bytes-long"), map[string]any{
		"exp": time.Now().Add(-1 * time.Minute).Unix(),
	})
	res, _, why := v.Verify(context.Background(), token)
	if res != InvalidToken {
		t.Fatalf("expected InvalidToken for expired token; got %v (%s)", res, why)
	}
}

func TestVerify_AlgConfusionDefense(t *testing.T) {
	// HS256-configured verifier must reject an unsigned token even if its
	// alg header claims "none". jose v4 enforces alg via ParseSigned, but
	// we double-check our wiring.
	v, _ := NewVerifier(JWTConfig{Algorithm: "HS256", HMACSecret: "supersecret-must-be-at-least-32-bytes-long"})
	_, _, why := v.Verify(context.Background(), "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ1MSJ9.")
	if why == "ok" {
		t.Fatalf("none-alg token should not verify")
	}
}

func TestVerify_RequireClaims(t *testing.T) {
	v, _ := NewVerifier(JWTConfig{
		Algorithm:     "HS256",
		HMACSecret:    "supersecret-must-be-at-least-32-bytes-long",
		RequireClaims: map[string]string{"role": "admin"},
	})
	token := sign(t, jose.HS256, []byte("supersecret-must-be-at-least-32-bytes-long"), map[string]any{
		"role": "user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	res, _, _ := v.Verify(context.Background(), token)
	if res != MissingClaims {
		t.Fatalf("expected MissingClaims; got %v", res)
	}
}

func TestVerify_JWKS_RSA(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: priv.Public(), KeyID: "k1", Algorithm: "RS256", Use: "sig"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer srv.Close()

	v, err := NewVerifier(JWTConfig{Algorithm: "RS256", JWKSURL: srv.URL})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	signer, _ := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", "k1"),
	)
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "u1"})
	obj, _ := signer.Sign(payload)
	token, _ := obj.CompactSerialize()

	res, _, why := v.Verify(context.Background(), token)
	if res != OK {
		t.Fatalf("expected OK; got %v (%s)", res, why)
	}
}

func TestVerify_AudArrayMatch(t *testing.T) {
	v, _ := NewVerifier(JWTConfig{Algorithm: "HS256", HMACSecret: "supersecret-must-be-at-least-32-bytes-long", Audience: "billing"})
	token := sign(t, jose.HS256, []byte("supersecret-must-be-at-least-32-bytes-long"), map[string]any{
		"aud": []any{"identity", "billing", "audit"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	res, _, why := v.Verify(context.Background(), token)
	if res != OK {
		t.Fatalf("expected OK with aud array; got %v (%s)", res, why)
	}
}
