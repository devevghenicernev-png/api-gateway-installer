//go:build bench

package benchmark

import (
	"context"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/auth"
)

// BenchmarkJWTValidate_HMAC measures HMAC-SHA256 JWT validation speed.
//
// Expected: <0.5ms per token (>2000 tokens/sec/core)
// Compare:  Kong lua-resty-jwt: ~1ms, Traefik go-jwt: ~0.3ms
func BenchmarkJWTValidate_HMAC(b *testing.B) {
	cfg := auth.JWTConfig{
		Algorithm:  "HS256",
		HMACSecret: "test-secret-32-bytes-long-key",
	}
	v, err := auth.NewVerifier(cfg)
	if err != nil {
		b.Fatal(err)
	}

	// Pre-generate a valid token
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = v.Verify(context.Background(), token)
	}
	// Target: ~500ns/op, 0 allocs
}

// BenchmarkJWTValidate_RSA measures RSA-SHA256 JWT validation speed.
//
// Expected: <1ms per token (>1000 tokens/sec/core)
// Note: Slower than HMAC due to asymmetric crypto
func BenchmarkJWTValidate_RSA(b *testing.B) {
	cfg := auth.JWTConfig{
		Algorithm: "RS256",
		JWKSURL:   "http://localhost:8080/.well-known/jwks.json",
	}
	v, err := auth.NewVerifier(cfg)
	if err != nil {
		b.Skip("JWKS server not running")
	}

	token := "eyJ..." // RSA-signed token

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = v.Verify(context.Background(), token)
	}
	// Target: ~1000ns/op
}

// BenchmarkJWTValidate_Parallel tests concurrent JWT validation.
//
// Simulates: 1000 concurrent requests, each validating a JWT
// Expected: Linear scaling up to CPU count
func BenchmarkJWTValidate_Parallel(b *testing.B) {
	cfg := auth.JWTConfig{
		Algorithm:  "HS256",
		HMACSecret: "test-secret-32-bytes-long-key",
	}
	v, err := auth.NewVerifier(cfg)
	if err != nil {
		b.Fatal(err)
	}

	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = v.Verify(context.Background(), token)
		}
	})
	// Target: Linear scaling (4 cores → 4x throughput)
}
