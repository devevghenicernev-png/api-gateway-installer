// Session-cookie handlers: the auth_request endpoint nginx hits on
// every request to a session-protected route, plus the admin CRUD
// endpoints an upstream login flow (or operator) uses to mint and
// revoke sessions.
//
// The auth_request endpoint is read-mostly (Lookup) and may be hit
// thousands of times per second; the admin endpoints are write-heavy
// and rare.
package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/auth"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

// defaultSessionTTL is used when Security.Sessions.TTL is zero.
const defaultSessionTTL = 24 * time.Hour

// defaultSessionCookie is used when Security.Sessions.CookieName is empty.
const defaultSessionCookie = "apigw_session"

// handleSessionAuth backs nginx auth_request /auth/session/<api>. The
// upstream API must have Session=true; otherwise we 500 (treating
// "wrong config" as fail-closed). The cookie name + sliding window
// come from Security.Sessions.
func (s *Server) handleSessionAuth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/session/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		s.Logger.Error("session: load config", "err", err)
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
	if apiCfg == nil || !apiCfg.Session {
		s.Logger.Warn("session: api not session-enabled", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if cfg.Security.Sessions == nil {
		s.Logger.Warn("session: Security.Sessions not configured", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if err := s.ensureSessionStore(cfg); err != nil {
		s.Logger.Error("session: store open", "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	cookieName := cfg.Security.Sessions.CookieName
	if cookieName == "" {
		cookieName = defaultSessionCookie
	}
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	sliding := time.Duration(0)
	if cfg.Security.Sessions.SlidingWindow {
		sliding = cfg.Security.Sessions.TTL
		if sliding <= 0 {
			sliding = defaultSessionTTL
		}
	}
	sess, err := s.SessionStore.Lookup(c.Value, sliding)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) || errors.Is(err, auth.ErrSessionNotFound) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.Logger.Error("session: lookup", "api", apiName, "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	if !s.runACL(w, apiCfg, apiName, auth.ACLMatchSubject, sess.Subject, "session") {
		return
	}

	w.Header().Set("X-Apigw-Subject", sess.Subject)
	w.Header().Set("X-Apigw-Session-ID", sess.ID)
	if len(sess.Scopes) > 0 {
		w.Header().Set("X-Apigw-Scopes", strings.Join(sess.Scopes, " "))
	}
	w.WriteHeader(http.StatusOK)
}

// ---------- /api/admin/sessions ----------

type sessionIssueRequest struct {
	Subject string            `json:"subject"`
	Scopes  []string          `json:"scopes,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
	TTLSec  int64             `json:"ttl_sec,omitempty"` // override Security.Sessions.TTL
}

type sessionIssueResponse struct {
	Session   *auth.Session `json:"session"`
	SetCookie string        `json:"set_cookie"` // header value the caller should pass back to the browser
}

func (s *Server) adminSessionsHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.Security.Sessions == nil {
		adminWriteJSONError(w, http.StatusServiceUnavailable, "security.sessions not configured")
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	if err := s.ensureSessionStore(cfg); err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, "session store: "+err.Error())
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.handleSessionIssue(w, r, cfg)
	case http.MethodGet:
		s.handleSessionList(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (s *Server) handleSessionIssue(w http.ResponseWriter, r *http.Request, cfg *config.Config) {
	if s.Sec != nil {
		if _, err := s.Sec.Guard(r, Action{Permission: "session.create"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
	}
	var req sessionIssueRequest
	if err := s.Sec.ReadBody(r, &req); err != nil {
		adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Subject == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "subject required")
		return
	}
	ttl := cfg.Security.Sessions.TTL
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	if req.TTLSec > 0 {
		ttl = time.Duration(req.TTLSec) * time.Second
	}
	sess, err := s.SessionStore.Issue(req.Subject, req.Scopes, req.Meta, ttl)
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, "issue: "+err.Error())
		return
	}
	cookieName := cfg.Security.Sessions.CookieName
	if cookieName == "" {
		cookieName = defaultSessionCookie
	}
	// Secure/HttpOnly come from config — operators flip them off only for
	// local dev. SameSite has its own enum mapping below. gosec G124 wants
	// hard-coded `true` literals; we trust the config schema instead.
	cookie := &http.Cookie{ //nolint:gosec // Secure/HttpOnly defaults set in config.Sessions; dev override is intentional
		Name:     cookieName,
		Value:    sess.ID,
		Path:     "/",
		Domain:   cfg.Security.Sessions.Domain,
		Expires:  sess.ExpiresAt,
		MaxAge:   int(ttl.Seconds()),
		Secure:   cfg.Security.Sessions.Secure,
		HttpOnly: cfg.Security.Sessions.HTTPOnly,
		SameSite: parseSameSite(cfg.Security.Sessions.SameSite),
	}
	// Surface the cookie via Set-Cookie AND in the JSON body so a
	// machine-to-machine login proxy can decide which to use.
	http.SetCookie(w, cookie)
	adminWriteJSON(w, http.StatusOK, sessionIssueResponse{
		Session:   sess,
		SetCookie: cookie.String(),
	})
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if s.Sec != nil {
		if _, err := s.Sec.Guard(r, Action{Permission: "session.list"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
	}
	subject := r.URL.Query().Get("subject")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		if n > 0 && n <= 1000 {
			limit = n
		}
	}
	out, err := s.SessionStore.List(subject, limit)
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, "list: "+err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"total":    s.SessionStore.Size(),
	})
}

// adminSessionHandler is DELETE /api/admin/sessions/<id> — revoke one.
func (s *Server) adminSessionHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.Security.Sessions == nil {
		adminWriteJSONError(w, http.StatusServiceUnavailable, "security.sessions not configured")
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	if err := s.ensureSessionStore(cfg); err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, "session store: "+err.Error())
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "use DELETE")
		return
	}
	if s.Sec != nil {
		if _, err := s.Sec.Guard(r, Action{Permission: "session.revoke"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "session id required")
		return
	}
	if err := s.SessionStore.Revoke(id); err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, "revoke: "+err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]string{"revoked": id})
}

func parseSameSite(s string) http.SameSite {
	switch strings.ToLower(s) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// Static-analysis nudge: tests sometimes import these via marshalling
// paths without ever calling them; this keeps gopls happy.
var _ = json.Marshal
