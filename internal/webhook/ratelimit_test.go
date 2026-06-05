package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAllowRateUnlimited(t *testing.T) {
	s := &Server{RateLimitPerMin: 0}
	for i := 0; i < 10000; i++ {
		if !s.allowRate("d") {
			t.Fatalf("zero limit must allow everything; refused at %d", i)
		}
	}
}

func TestAllowRateBucketFill(t *testing.T) {
	s := &Server{RateLimitPerMin: 3}
	for i := 0; i < 3; i++ {
		if !s.allowRate("d") {
			t.Fatalf("request %d should be allowed within budget", i)
		}
	}
	if s.allowRate("d") {
		t.Fatal("4th request should be throttled")
	}
}

func TestAllowRatePerDeployIsolation(t *testing.T) {
	s := &Server{RateLimitPerMin: 2}
	// fill bucket for "a"
	if !s.allowRate("a") || !s.allowRate("a") {
		t.Fatal("budget for a")
	}
	if s.allowRate("a") {
		t.Fatal("a should be throttled")
	}
	// "b" still has full budget
	if !s.allowRate("b") || !s.allowRate("b") {
		t.Fatal("b should have its own budget")
	}
}

func TestAllowRateWindowRollover(t *testing.T) {
	s := &Server{RateLimitPerMin: 1}
	if !s.allowRate("d") {
		t.Fatal("first request")
	}
	if s.allowRate("d") {
		t.Fatal("second request should be throttled")
	}
	// Forge the window start to be > 1 minute ago, simulating natural rollover.
	s.rateMu.Lock()
	s.rate["d"].windowStart = time.Now().Add(-2 * time.Minute)
	s.rateMu.Unlock()
	if !s.allowRate("d") {
		t.Fatal("window should have rolled over")
	}
}

// TestServeHTTP429 wires the full handler and confirms a real 429 + Retry-After
// is returned once the bucket is full.
func TestServeHTTP429(t *testing.T) {
	logger := slog.Default()
	s := New(":0", func(_ context.Context, _ string, _ string, _ []byte) error {
		return nil
	}, logger)
	s.RateLimitPerMin = 1
	mux := http.NewServeMux()
	s.Routes(mux)

	body := []byte(`{}`)
	sig := signGitHub("dummy-secret", body)
	// First call needs a valid secret on disk; instead we skip past LoadSecret
	// by hitting an unknown deploy → 404 short-circuits before rate counts.
	// So rate limit fires BEFORE the secret check. Verify by sending 2 to the
	// same unknown deploy: 1st gets 429-or-404, 2nd gets 429.
	for i := 0; i < 2; i++ {
		req := newSignedReq("/webhook/unknown-deploy", body, sig)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if i == 1 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("call %d expected 429, got %d (%s)", i, w.Code, w.Body.String())
		}
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Fatal("429 must include Retry-After")
		}
	}
}

func TestStatsIncludesThrottled(t *testing.T) {
	s := New(":0", func(_ context.Context, _ string, _ string, _ []byte) error { return nil }, slog.Default())
	s.RateLimitPerMin = 1
	mux := http.NewServeMux()
	s.Routes(mux)

	for i := 0; i < 3; i++ {
		req := newSignedReq("/webhook/foo", []byte(`{}`), signGitHub("k", []byte(`{}`)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
	}
	if s.Stats().Throttled < 1 {
		t.Fatalf("expected throttled stat >= 1, got %d", s.Stats().Throttled)
	}
}

// helpers

func newSignedReq(path string, body []byte, sig string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	r.Header.Set("X-Hub-Signature-256", sig)
	r.Header.Set("X-GitHub-Delivery", "test-delivery-id")
	r.Header.Set("X-GitHub-Event", "push")
	return r
}

func signGitHub(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}
