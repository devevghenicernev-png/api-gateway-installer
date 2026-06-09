package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxBodyBytes caps the request body apigw will read.
//
// GitHub webhook payloads are typically < 50 KB but the upper bound is 25 MB
// (their docs). We accept 1 MB — enough for any push event, small enough
// that a malicious caller can't flood us. Larger payloads (releases with
// notes, etc.) we drop with 413.
const MaxBodyBytes = 1 << 20 // 1 MiB

// PayloadHandler is invoked AFTER HMAC verification + replay check pass.
// It must be cheap (<2s) — long work goes through the queue.
type PayloadHandler func(ctx context.Context, deploy string, event string, body []byte) error

// Server wraps the http.Server with apigw-specific deps.
type Server struct {
	Addr    string
	Logger  *slog.Logger
	Handler PayloadHandler // called after HMAC + replay pass

	// Publish, when non-nil, is invoked once for every accepted webhook.
	// The dashboard wires this to events.Hub.Publish so the live activity
	// feed updates without polling. Optional — webhook-only mode leaves nil.
	Publish func(topic, evType string, data []byte)

	// Metrics, when non-nil, receives Prometheus counter increments for
	// every accepted / replayed / mismatched / unsigned delivery.
	Metrics metricsSink

	// RateLimitPerMin caps deliveries per (deploy, minute) once HMAC has
	// passed. 0 = no limit. The default at the constructor level is 100,
	// which is far above GitHub's effective push frequency but defends
	// against runaway loops in CI pipelines and bot-driven traffic.
	RateLimitPerMin int

	replay *ReplayCache
	srv    *http.Server

	// rate is a per-deploy token bucket map keyed by deploy name; lazily
	// allocated to keep zero-value Server cheap.
	rateMu sync.Mutex
	rate   map[string]*rateBucket

	// Stats — also published to the dashboard via the event hub.
	received   atomic.Uint64
	unsigned   atomic.Uint64
	replayed   atomic.Uint64
	mismatched atomic.Uint64
	accepted   atomic.Uint64
	throttled  atomic.Uint64
}

// rateBucket is a simple fixed-window counter — perfectly adequate for the
// "stop a runaway CI" use case without needing a token-bucket library.
type rateBucket struct {
	windowStart time.Time
	count       int
}

// metricsSink is the minimal surface webhook.Server depends on — keeps
// the actual *metrics.Metrics type out of this package to avoid a cycle
// (events.Hub also depends on metrics indirectly).
type metricsSink interface {
	IncWebhooksAccepted(deploy string)
	IncWebhooksReplayed()
	IncWebhookHMACFails()
	IncWebhookUnsigned()
}

// DefaultRateLimitPerMin is the per-deploy ceiling applied by New().
// Override via Server.RateLimitPerMin after construction.
const DefaultRateLimitPerMin = 100

// New returns a Server bound to `addr`. The Handler is called for every
// validated webhook; pass a function that enqueues into the bbolt queue.
func New(addr string, h PayloadHandler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		Addr:            addr,
		Logger:          logger,
		Handler:         h,
		replay:          NewReplayCache(),
		RateLimitPerMin: DefaultRateLimitPerMin,
		rate:            map[string]*rateBucket{},
	}
}

// Stats is what `apigw webhook status` reads. Snapshot, monotonic.
type Stats struct {
	Received   uint64 `json:"received"`
	Unsigned   uint64 `json:"unsigned_rejected"`
	Replayed   uint64 `json:"replays_dropped"`
	Mismatched uint64 `json:"hmac_mismatches"`
	Accepted   uint64 `json:"accepted"`
	Throttled  uint64 `json:"throttled"`
	ReplayLen  int    `json:"replay_lru_size"`
}

// Stats returns the current counters.
func (s *Server) Stats() Stats {
	return Stats{
		Received:   s.received.Load(),
		Unsigned:   s.unsigned.Load(),
		Replayed:   s.replayed.Load(),
		Mismatched: s.mismatched.Load(),
		Accepted:   s.accepted.Load(),
		Throttled:  s.throttled.Load(),
		ReplayLen:  s.replay.Len(),
	}
}

// allowRate is a fixed-window-per-minute admission check. Returns true when
// the request is within budget, false when it's been throttled. The window
// auto-rolls each minute so throttled bursts recover automatically.
func (s *Server) allowRate(deploy string) bool {
	if s.RateLimitPerMin <= 0 {
		return true
	}
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rate == nil {
		s.rate = map[string]*rateBucket{}
	}
	b, ok := s.rate[deploy]
	now := time.Now()
	if !ok || now.Sub(b.windowStart) >= time.Minute {
		s.rate[deploy] = &rateBucket{windowStart: now, count: 1}
		return true
	}
	if b.count >= s.RateLimitPerMin {
		return false
	}
	b.count++
	return true
}

// Routes registers handlers on the given mux. Split out for testability so
// callers can mount apigw alongside other handlers.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/webhook/", s.handle)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
}

// ListenAndServe blocks until ctx is cancelled. Used by `apigw webhook serve`.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	s.Routes(mux)
	s.srv = &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handle is the single entrypoint for all /webhook/<deploy> requests.
//
// Sequence:
//  1. Method must be POST.
//  2. Extract deploy name from URL path.
//  3. Load the per-deploy secret (404 if no such deploy / no secret).
//  4. Read body (<= MaxBodyBytes).
//  5. Verify HMAC. Reject 401 on missing/invalid (NOT 400 — don't leak which).
//  6. Check X-GitHub-Delivery against replay cache. Already seen → 200 (idempotent).
//  7. Pass to handler (which enqueues to bbolt).
//  8. Respond 202 Accepted.
//
// We aim for <2 s end-to-end. The handler MUST NOT do the deploy inline —
// that's a 5 min+ operation and would trip GitHub's 10 s timeout.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.received.Add(1)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	deploy := strings.TrimPrefix(r.URL.Path, "/webhook/")
	deploy = strings.TrimSuffix(deploy, "/")
	if deploy == "" || strings.ContainsAny(deploy, "/.") {
		http.NotFound(w, r)
		return
	}

	if !s.allowRate(deploy) {
		s.throttled.Add(1)
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	secrets, err := LoadValidSecrets(deploy)
	if err != nil {
		// Don't leak whether the deploy exists or just lacks a secret.
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > MaxBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	sig := r.Header.Get("X-Hub-Signature-256")
	if sig == "" {
		s.unsigned.Add(1)
		if s.Metrics != nil {
			s.Metrics.IncWebhookUnsigned()
		}
		// 401 (not 400) — fail closed, don't differentiate missing vs invalid.
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !verifyAny(secrets, body, sig) {
		s.mismatched.Add(1)
		if s.Metrics != nil {
			s.Metrics.IncWebhookHMACFails()
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	delivery := r.Header.Get("X-GitHub-Delivery")
	if s.replay.TestAndAdd(delivery) {
		s.replayed.Add(1)
		if s.Metrics != nil {
			s.Metrics.IncWebhooksReplayed()
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	event := r.Header.Get("X-GitHub-Event")

	if s.Handler != nil {
		hctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Handler(hctx, deploy, event, body); err != nil {
			// Handler failed (e.g. bbolt write error). Roll the replay
			// entry back so GitHub's retry within the window WILL be
			// re-processed instead of silently 200'd.
			s.replay.Forget(delivery)
			s.Logger.Error("webhook handler",
				slog.String("deploy", deploy),
				slog.String("event", event),
				slog.String("delivery", delivery),
				slog.String("err", err.Error()))
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	}
	s.accepted.Add(1)
	if s.Metrics != nil {
		s.Metrics.IncWebhooksAccepted(deploy)
	}
	s.Logger.Info("webhook accepted",
		slog.String("deploy", deploy),
		slog.String("event", event),
		slog.String("delivery", delivery))

	if s.Publish != nil {
		// Single-line JSON per events package contract.
		payload, _ := json.Marshal(map[string]any{
			"deploy":   deploy,
			"event":    event,
			"delivery": delivery,
			"ts":       time.Now().Unix(),
		})
		s.Publish("webhook.recv", "webhook", payload)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "accepted",
		"deploy":   deploy,
		"event":    event,
		"delivery": delivery,
	})
}

// PublicURL returns the URL operators paste into GitHub's webhook setup form
// for `deploy`, given the public hostname.
func PublicURL(host, deploy string, https bool) string {
	scheme := "http"
	if https {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/webhook/%s", scheme, host, deploy)
}

// ResolveListenAddr expands a port-only spec ("9000") into a full address
// ("0.0.0.0:9000") and validates it parses. Used by `webhook serve`.
func ResolveListenAddr(spec string) (string, error) {
	if spec == "" {
		return ":9000", nil
	}
	// If it's just a number, prefix with ":".
	if _, err := strconv.Atoi(spec); err == nil {
		return ":" + spec, nil
	}
	if _, _, err := net.SplitHostPort(spec); err != nil {
		return "", fmt.Errorf("invalid listen address %q: %w", spec, err)
	}
	return spec, nil
}
