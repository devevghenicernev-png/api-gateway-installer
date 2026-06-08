// Package dashboard is apigw's live web UI. One Go process owns:
//
//   - the events.Hub (pub/sub fan-out),
//   - the SSE handler (/events),
//   - the static UI served from go:embed,
//   - the JSON API the UI talks to.
//
// It replaces the 765-LoC server.js from the bash version. Single binary,
// no Node.js runtime.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/assets"
	"github.com/devevghenicernev-png/apigw/internal/auth"
	"github.com/devevghenicernev-png/apigw/internal/build"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/events"
	"github.com/devevghenicernev-png/apigw/internal/metrics"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

// DefaultOCSPCachePath is where the dashboard opens its OCSP cache
// when Security.StateDir is unset. Mirrors audit.db / approvals.db /
// jobs.db locations.
const DefaultOCSPCachePath = "/var/lib/apigw/ocsp.db"

// DefaultSessionsPath mirrors DefaultOCSPCachePath for the session
// store.
const DefaultSessionsPath = "/var/lib/apigw/sessions.db"

// Server bundles the hub + HTTP mux + dependencies. One per process.
type Server struct {
	Addr     string
	Hub      *events.Hub
	Logger   *slog.Logger
	ConfigFn func() (*config.Config, error) // fresh read each call (long-running)

	srv *http.Server

	// Start time for uptime reporting.
	started time.Time

	// Pending counter — number of currently-attached SSE clients.
	clients connCount

	// Metrics is the Prometheus collector. Non-nil when MetricsEnabled was
	// passed to New(). Wired into producers (webhook server, deploy
	// queue, events.Hub) via getters below.
	Metrics *metrics.Metrics

	// Sec is the integrated security layer (RBAC + audit + policy + approvals
	// + tenants + alerts). nil = legacy "no security" mode.
	Sec *Security

	// Queue is the durable webhook/deploy job queue. The admin "redeploy"
	// endpoint enqueues into it; the same worker that drains GitHub
	// deliveries picks the job up. nil = run-via-UI disabled, CLI still
	// works (it uses an in-process deploy.JobQueue).
	Queue *webhook.Queue

	// hmacNonces holds the per-API nonce LRU for HMAC replay protection.
	// Lazily inited in New(); never nil after construction.
	hmacNonces *auth.HMACNonces

	// OCSPCache is the bbolt-backed cache of client-cert OCSP responses.
	// Lazily opened on first use by the mTLS handler (so installs without
	// OCSP-enabled APIs pay no I/O cost). Nil = not yet opened.
	OCSPCache *apitls.OCSPCache

	// ocspHTTP is the outbound HTTP client used to fetch fresh OCSP
	// responses. 5s timeout is the responder-industry norm.
	ocspHTTP *http.Client

	// SessionStore is the cookie-based session store. Lazily opened on
	// first /auth/session/* or /api/admin/sessions* hit. Nil = not yet
	// opened or Security.Sessions disabled.
	SessionStore *auth.SessionStore

	// OIDCFetcher / SSOHTTPClient are injectable for the SSO login flow.
	// Tests stub these out; production uses sensible defaults
	// (NewOIDCFetcher + 10s-timeout client).
	OIDCFetcher   *auth.OIDCFetcher
	SSOHTTPClient *http.Client

	// ssoStateStoreInst tracks in-flight OIDC login attempts (state +
	// nonce). Lazily created on first /sso/login hit.
	ssoStateStoreInst *ssoStateStore
}

// New constructs a Server bound to `addr`.
//
// `m` is the central Prometheus collector — pass the same instance into
// webhook.Server.Metrics, events.Hub.Metrics, deploy.ApplyRequest.Metrics,
// nginx.Manager.Metrics so /metrics reflects the whole process. Pass nil
// to suppress the /metrics route entirely.
func New(addr string, hub *events.Hub, cfgFn func() (*config.Config, error), logger *slog.Logger, m *metrics.Metrics) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		Addr:       addr,
		Hub:        hub,
		Logger:     logger,
		ConfigFn:   cfgFn,
		started:    time.Now(),
		Metrics:    m,
		hmacNonces: auth.NewHMACNonces(),
		ocspHTTP:   &http.Client{Timeout: 5 * time.Second},
	}
}

// ocspCachePath resolves where the OCSP bbolt DB lives. Mirrors the
// audit/approvals StateDir convention.
func ocspCachePath(cfg *config.Config) string {
	if cfg != nil && cfg.Security.StateDir != "" {
		return cfg.Security.StateDir + "/ocsp.db"
	}
	return DefaultOCSPCachePath
}

// ensureOCSPCache opens the bbolt cache on first use; subsequent calls
// are a no-op. Returns an error only the first time around — callers
// must handle a nil cache by either soft-failing or returning 500.
func (s *Server) ensureOCSPCache(cfg *config.Config) error {
	if s.OCSPCache != nil {
		return nil
	}
	c, err := apitls.OpenOCSPCache(ocspCachePath(cfg))
	if err != nil {
		return err
	}
	s.OCSPCache = c
	return nil
}

// sessionsPath resolves where the session bbolt DB lives.
func sessionsPath(cfg *config.Config) string {
	if cfg != nil && cfg.Security.StateDir != "" {
		return cfg.Security.StateDir + "/sessions.db"
	}
	return DefaultSessionsPath
}

// ensureSessionStore opens the bbolt-backed session store on first
// use. Subsequent calls are a no-op.
func (s *Server) ensureSessionStore(cfg *config.Config) error {
	if s.SessionStore != nil {
		return nil
	}
	st, err := auth.OpenSessionStore(sessionsPath(cfg))
	if err != nil {
		return err
	}
	s.SessionStore = st
	return nil
}

// Routes wires the mux. Split out for testability so callers can mount
// alongside other handlers if needed (e.g. embed in webhook server later).
func (s *Server) Routes(mux *http.ServeMux) {
	uiFS, _ := fs.Sub(assets.Dashboard(), "dashboard")
	mux.HandleFunc("/", s.staticHandler(uiFS))
	mux.HandleFunc("/events", s.sseWithCounter())
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/logs/", s.handleLogs)
	mux.HandleFunc("/api/admin/sso/login", s.handleSSOLogin)
	mux.HandleFunc("/api/admin/sso/callback", s.handleSSOCallback)
	mux.HandleFunc("/auth/jwt/", s.handleJWTAuth)
	mux.HandleFunc("/auth/mtls/", s.handleMTLSAuth)
	mux.HandleFunc("/auth/apikey/", s.handleAPIKeyAuth)
	mux.HandleFunc("/auth/hmac/", s.handleHMACAuth)
	mux.HandleFunc("/auth/session/", s.handleSessionAuth)
	mux.HandleFunc("/auth/oauth2/", s.handleOAuth2Auth)
	mux.HandleFunc("/mock/", s.handleMock)
	s.adminRoutes(mux)
	s.securityRoutes(mux)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if s.Metrics != nil {
		mux.Handle("/metrics", s.Metrics.Handler())
	}
}

// securityRoutes mounts the operator-facing audit/approvals/csrf endpoints.
// All require RBAC permissions when Security is configured.
func (s *Server) securityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/csrf", s.handleCSRF)
	mux.HandleFunc("/api/admin/audit", s.handleAuditQuery)
	mux.HandleFunc("/api/admin/approvals", s.handleApprovalsList)
	mux.HandleFunc("/api/admin/approvals/", s.handleApprovalOne)
	mux.HandleFunc("/api/admin/tenants", s.handleTenants)
	mux.HandleFunc("/api/admin/alerts/test", s.handleAlertTest)
}

// handleJWTAuth answers nginx auth_request sub-requests. Path:
// /auth/jwt/<api-name>. The original request's Authorization header arrives
// via proxy_set_header (template ensures it's forwarded). We look up the
// API's JWT config and run a Verifier; reply 200 / 401 / 403 / 500.
//
// Response body is empty — auth_request only honors the status code, never
// the body, so there's no reason to send anything.
//
// Verifiers are built per-request because:
//   - they're cheap (no expensive setup),
//   - config can change between requests (apigw api reload),
//   - JWKS caching lives inside Verifier so we'd lose it — but since we
//     don't memoize Verifier across calls, JWKS gets re-fetched on every
//     request. That's not ideal; TODO(perf): memoize verifiers by (api,
//     config-hash) once profiling shows it matters.
func (s *Server) handleJWTAuth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/jwt/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	cfg, err := s.ConfigFn()
	if err != nil {
		s.Logger.Error("jwt: load config", "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	var apiCfg *config.API
	for i := range cfg.APIs {
		if cfg.APIs[i].Name == apiName {
			apiCfg = &cfg.APIs[i]
			break
		}
	}
	if apiCfg == nil || apiCfg.JWT == nil {
		// Configured-out APIs: treat as misconfiguration on the nginx side.
		// Return 500 so operators see the issue (rather than silently 200).
		s.Logger.Warn("jwt: no config for api", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	verifier, err := auth.NewVerifier(auth.JWTConfig{
		Algorithm:     apiCfg.JWT.Algorithm,
		HMACSecret:    apiCfg.JWT.HMACSecret,
		JWKSURL:       apiCfg.JWT.JWKSURL,
		Issuer:        apiCfg.JWT.Issuer,
		Audience:      apiCfg.JWT.Audience,
		RequireClaims: apiCfg.JWT.RequireClaims,
	})
	if err != nil {
		s.Logger.Error("jwt: build verifier", "api", apiName, "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	res, claims, why := verifier.Verify(r.Context(), r.Header.Get("Authorization"))
	if res != auth.OK {
		s.Logger.Info("jwt: reject", "api", apiName, "result", res, "reason", why)
		w.WriteHeader(res.HTTPStatus())
		return
	}
	sub, _ := claims["sub"].(string)
	if !s.runACL(w, apiCfg, apiName, auth.ACLMatchSubject, sub, "jwt") {
		return
	}
	if !s.applyConsumer(w, cfg, apiCfg, apiName, sub, "jwt") {
		return
	}
	// On success, surface the subject as a response header so nginx can
	// `auth_request_set` and forward to the upstream (X-Remote-User pattern).
	if sub != "" {
		w.Header().Set("X-Apigw-Subject", sub)
	}
	w.WriteHeader(http.StatusOK)
}

// handleMTLSAuth is the auth_request endpoint for mTLS validation. nginx
// forwards $ssl_client_verify, $ssl_client_s_dn, $ssl_client_escaped_cert,
// and $ssl_client_fingerprint via headers (template wires this up). We
// re-verify against the API's allow-list and 200/403.
//
// On success the matched CN is set as X-Apigw-Client-CN so the upstream
// can see who's calling. nginx auth_request_set forwards it.
func (s *Server) handleMTLSAuth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/mtls/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		s.Logger.Error("mtls: load config", "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	var apiCfg *config.API
	for i := range cfg.APIs {
		if cfg.APIs[i].Name == apiName {
			apiCfg = &cfg.APIs[i]
			break
		}
	}
	if apiCfg == nil || apiCfg.MTLS == nil {
		s.Logger.Warn("mtls: no config for api", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	res, cn, why := auth.VerifyMTLS(auth.MTLSConfig{
		CAFile:            apiCfg.MTLS.CAFile,
		AllowCNs:          apiCfg.MTLS.AllowCNs,
		AllowSANs:         apiCfg.MTLS.AllowSANs,
		AllowFingerprints: apiCfg.MTLS.AllowFingerprints,
		Optional:          apiCfg.MTLS.Optional,
	}, r.Header)
	if res != auth.MTLSOK {
		s.Logger.Info("mtls: reject", "api", apiName, "result", res, "reason", why, "cn", cn)
		w.WriteHeader(res.HTTPStatus())
		return
	}

	if !s.runACL(w, apiCfg, apiName, auth.ACLMatchMTLSCN, cn, "mtls") {
		return
	}
	if !s.applyConsumer(w, cfg, apiCfg, apiName, cn, "mtls") {
		return
	}

	// Revocation check (RFC 6960). Off unless the API config has OCSPCheck.
	// On revoked → 403; on responder error → soft/hard-fail per config.
	if apiCfg.MTLS.OCSPCheck {
		ok, status, oerr := s.checkOCSPRevocation(apiName, apiCfg.MTLS, r.Header)
		if !ok {
			s.Logger.Info("mtls: ocsp deny", "api", apiName, "cn", cn, "status", status, "err", oerr)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if oerr != nil {
			// soft-fail path: oerr non-nil but ok=true.
			s.Logger.Warn("mtls: ocsp soft-fail", "api", apiName, "cn", cn, "err", oerr)
		}
	}

	if cn != "" {
		w.Header().Set("X-Apigw-Client-CN", cn)
	}
	w.WriteHeader(http.StatusOK)
}

// ListenAndServe blocks until ctx is cancelled. Used by `apigw dashboard serve`.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	s.Routes(mux)
	s.srv = &http.Server{
		Addr:              s.Addr,
		Handler:           withRequestLog(mux, s.Logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       0, // SSE — never close on read idle
		WriteTimeout:      0, // SSE — never close on write idle
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdown)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// sseWithCounter wraps Hub.SSEHandler with a connected-clients gauge.
//
// We can't count by checking goroutines or http.Server.ActiveConn — instead
// we increment on entry, defer decrement. Read via /api/status.
func (s *Server) sseWithCounter() http.HandlerFunc {
	inner := s.Hub.SSEHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		s.clients.inc()
		if s.Metrics != nil {
			s.Metrics.SetSSEClients(s.clients.get())
		}
		defer func() {
			s.clients.dec()
			if s.Metrics != nil {
				s.Metrics.SetSSEClients(s.clients.get())
			}
		}()
		inner(w, r)
	}
}

// staticHandler serves the embedded UI. SPA-style: any path not starting
// with /api/ or /events falls back to index.html so the JS router can take
// over later if we ever add one (v1 has no client-side routing).
func (s *Server) staticHandler(uiFS fs.FS) http.HandlerFunc {
	if uiFS == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "ui assets missing from binary", http.StatusInternalServerError)
		}
	}
	fileServer := http.FileServer(http.FS(uiFS))
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := uiFS.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback: serve index.html for unknown routes (SPA-friendly).
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	}
}

// StatusSnapshot is what /api/status returns. JSON-shaped so the dashboard
// can render the initial state before the SSE stream catches it up.
type StatusSnapshot struct {
	Version   string            `json:"version"`
	Commit    string            `json:"commit"`
	UptimeSec int64             `json:"uptime_sec"`
	Clients   int               `json:"sse_clients"`
	Deploys   []DeploySummary   `json:"deploys"`
	TLS       []apitls.CertInfo `json:"tls"`
	Webhook   WebhookSummary    `json:"webhook"`
	NowUnix   int64             `json:"now_unix"`
}

type DeploySummary struct {
	Name       string `json:"name"`
	Runtime    string `json:"runtime"`
	Port       int    `json:"port"`
	Path       string `json:"path"`
	LastSHA    string `json:"last_sha"`
	LastDeploy string `json:"last_deploy"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error,omitempty"`
}

type WebhookSummary struct {
	Enabled    bool `json:"enabled"`
	Port       int  `json:"port"`
	QueueDepth int  `json:"queue_depth"`
	DeadLetter int  `json:"dead_letter"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	snap := StatusSnapshot{
		Version:   build.Version,
		Commit:    build.Commit,
		UptimeSec: int64(time.Since(s.started).Seconds()),
		Clients:   s.clients.get(),
		NowUnix:   time.Now().Unix(),
		Webhook: WebhookSummary{
			Enabled: cfg.Webhook.Enabled,
			Port:    cfg.Webhook.Port,
		},
	}

	for _, d := range cfg.Deploys {
		snap.Deploys = append(snap.Deploys, DeploySummary{
			Name:       d.Name,
			Runtime:    d.Runtime,
			Port:       d.Port,
			Path:       d.Path,
			LastSHA:    d.LastSHA,
			LastDeploy: d.LastDeploy,
			LastStatus: d.LastStatus,
			LastError:  d.LastError,
		})
	}
	if certs, err := apitls.ListCerts(); err == nil {
		snap.TLS = certs
	}
	if q, qerr := webhook.OpenQueue(); qerr == nil {
		snap.Webhook.QueueDepth, snap.Webhook.DeadLetter, _ = q.Depth()
		_ = q.Close()
	}

	writeJSON(w, snap)
}

// handleLogs returns the in-memory ring snapshot for a deploy's stdout.
// URL: /api/logs/<name>?lines=N (default 100).
//
// This is the historical-fetch path for `apigw deploy logs --lines N`
// (Phase 5+). Live-streaming uses /events instead.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.ContainsAny(name, "/.") {
		http.NotFound(w, r)
		return
	}
	n := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 && k <= 10_000 {
			n = k
		}
	}
	topic := fmt.Sprintf("deploy.%s.stdout", name)
	evs := s.Hub.Snapshot([]string{topic}, n)
	writeJSON(w, evs)
}

// withRequestLog wraps handler with slog request logging. Used at the mux
// root so every endpoint gets uniform structured logs in journald.
func withRequestLog(h http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SSE requests log on connect only — they live for hours, logging on
		// close fills journald with stale lines.
		if r.URL.Path == "/events" {
			logger.Info("sse open",
				slog.String("remote", r.RemoteAddr),
				slog.String("topics", r.URL.RawQuery))
			h.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		h.ServeHTTP(w, r)
		logger.Info("http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("took", time.Since(start)))
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		// We've already written headers, so we can't change status. Best we
		// can do is log via slog so operators see encode failures (OOM, I/O
		// timeout to the SSE client) instead of a silent 200 with truncated
		// body. withRequestLog runs at the mux root and won't see this.
		slog.Default().Warn("response encode", "err", err)
	}
}
