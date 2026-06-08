// Admin-dashboard SSO — interactive OIDC login that mints a session
// cookie via the existing internal/auth.SessionStore.
//
// Flow:
//
//	GET /api/admin/sso/login    → 302 to IdP's authorize endpoint
//	GET /api/admin/sso/callback → exchange code, validate ID token,
//	                              mint session, 302 to RedirectAfter
//
// State + nonce are stamped into a short-lived in-memory map; the cookie
// the browser carries between login and callback holds just the state
// key. We deliberately don't persist state in bbolt — 5-minute window
// memory is enough, and a process restart between login and callback
// would have invalidated the IdP's code anyway.
//
// SAML lives on the same Security.SSO struct but is unimplemented
// (returns 501) until a follow-up commit. OIDC covers Google, Okta,
// Auth0, Keycloak, AzureAD — the bulk of real-world IdPs.

package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/auth"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

// ssoStateTTL caps how long a login-in-flight can sit between
// /sso/login and /sso/callback. 5 minutes matches what most IdPs grant
// for their auth code.
const ssoStateTTL = 5 * time.Minute

// ssoStateCookie carries the per-attempt state key the browser sends
// back on /sso/callback. Short, transient, HttpOnly.
const ssoStateCookie = "apigw_sso_state"

// ssoState is one in-flight login attempt.
type ssoState struct {
	State        string
	Nonce        string
	RedirectBack string
	Expires      time.Time
}

// ssoStateStore is a tiny TTL map. Single-process — restarts drop in-
// flight logins, which is fine (IdP token would have expired anyway).
type ssoStateStore struct {
	mu    sync.Mutex
	items map[string]ssoState
}

func newSSOStateStore() *ssoStateStore { return &ssoStateStore{items: map[string]ssoState{}} }

func (s *ssoStateStore) put(state ssoState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[state.State] = state
	// Lazy eviction on every write — cheap, no GC goroutine.
	now := time.Now()
	for k, v := range s.items {
		if now.After(v.Expires) {
			delete(s.items, k)
		}
	}
}

func (s *ssoStateStore) take(key string) (ssoState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[key]
	if !ok {
		return ssoState{}, false
	}
	delete(s.items, key)
	if time.Now().After(v.Expires) {
		return ssoState{}, false
	}
	return v, true
}

// handleSSOLogin builds the authorization URL and 302s to the IdP.
func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		http.Error(w, "config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sso := cfg.Security.SSO
	if sso == nil {
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}
	switch strings.ToLower(sso.Provider) {
	case "oidc":
	case "saml":
		http.Error(w, "SAML SSO not yet implemented", http.StatusNotImplemented)
		return
	default:
		http.Error(w, "unknown SSO provider: "+sso.Provider, http.StatusBadRequest)
		return
	}
	disc, err := s.ssoDiscover(r.Context(), sso.IssuerURL)
	if err != nil {
		http.Error(w, "discover: "+err.Error(), http.StatusBadGateway)
		return
	}
	if disc.AuthorizationURL == "" {
		http.Error(w, "discovery missing authorization_endpoint", http.StatusBadGateway)
		return
	}
	state, _ := randomToken(24)
	nonce, _ := randomToken(24)
	s.ssoStates().put(ssoState{
		State:        state,
		Nonce:        nonce,
		RedirectBack: r.URL.Query().Get("redirect"),
		Expires:      time.Now().Add(ssoStateTTL),
	})
	scopes := sso.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", sso.ClientID)
	q.Set("redirect_uri", sso.RedirectURL)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	target := disc.AuthorizationURL
	if strings.Contains(target, "?") {
		target += "&" + q.Encode()
	} else {
		target += "?" + q.Encode()
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows Sessions config; SameSite=Lax + HttpOnly set
		Name:     ssoStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   cfg.Security.Sessions != nil && cfg.Security.Sessions.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ssoStateTTL.Seconds()),
	})
	http.Redirect(w, r, target, http.StatusFound)
}

// handleSSOCallback exchanges the code, validates the ID token, mints a
// session, sets the cookie, and 302s back to the redirect target.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		http.Error(w, "config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sso := cfg.Security.SSO
	if sso == nil {
		http.Error(w, "SSO not configured", http.StatusNotFound)
		return
	}
	cookie, err := r.Cookie(ssoStateCookie)
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	queryState := r.URL.Query().Get("state")
	if queryState == "" || queryState != cookie.Value {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	saved, ok := s.ssoStates().take(queryState)
	if !ok {
		http.Error(w, "state expired or unknown", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		errMsg := r.URL.Query().Get("error_description")
		if errMsg == "" {
			errMsg = r.URL.Query().Get("error")
		}
		http.Error(w, "no code in callback: "+errMsg, http.StatusBadRequest)
		return
	}
	disc, err := s.ssoDiscover(r.Context(), sso.IssuerURL)
	if err != nil {
		http.Error(w, "discover: "+err.Error(), http.StatusBadGateway)
		return
	}

	tok, err := s.ssoExchangeCode(r.Context(), disc.TokenURL, sso, code)
	if err != nil {
		http.Error(w, "token exchange: "+err.Error(), http.StatusBadGateway)
		return
	}
	if tok.IDToken == "" {
		http.Error(w, "no id_token returned", http.StatusBadGateway)
		return
	}
	claims, err := s.ssoParseIDToken(tok.IDToken, sso, disc)
	if err != nil {
		http.Error(w, "id_token invalid: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if claims.Nonce != saved.Nonce {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}

	subject := pickSubject(claims)
	if subject == "" {
		http.Error(w, "id_token missing subject", http.StatusUnauthorized)
		return
	}
	roles := mapRoles(claims, sso)
	if len(roles) == 0 {
		s.Logger.Warn("sso: no roles mapped for subject", slog.String("sub", subject))
	}

	if err := s.ensureSessionStore(cfg); err != nil {
		http.Error(w, "session store: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ttl := defaultSessionTTL
	if cfg.Security.Sessions != nil && cfg.Security.Sessions.TTL > 0 {
		ttl = cfg.Security.Sessions.TTL
	}
	meta := map[string]string{"sso": "oidc", "issuer": claims.Issuer}
	if email := claims.Email; email != "" {
		meta["email"] = email
	}
	if len(roles) > 0 {
		meta["roles"] = strings.Join(roles, ",")
	}
	sess, err := s.SessionStore.Issue(subject, roles, meta, ttl)
	if err != nil {
		http.Error(w, "session issue: "+err.Error(), http.StatusInternalServerError)
		return
	}

	cookieName := defaultSessionCookie
	if cfg.Security.Sessions != nil && cfg.Security.Sessions.CookieName != "" {
		cookieName = cfg.Security.Sessions.CookieName
	}
	secure := true
	httpOnly := true
	if cfg.Security.Sessions != nil {
		secure = cfg.Security.Sessions.Secure
		httpOnly = cfg.Security.Sessions.HTTPOnly
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure/HttpOnly driven by Sessions config
		Name:     cookieName,
		Value:    sess.ID,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Clear the transient state cookie.
	http.SetCookie(w, &http.Cookie{Name: ssoStateCookie, Value: "", Path: "/", MaxAge: -1}) //nolint:gosec // expiring cookie — value is empty, Secure/SameSite don't apply on deletion

	target := saved.RedirectBack
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// ssoDiscover resolves the OIDC discovery doc. Uses Server.ssoFetcher when
// set (tests inject a fake); falls back to the lazy package-level one.
func (s *Server) ssoDiscover(ctx context.Context, issuerURL string) (auth.OIDCDiscovery, error) {
	return s.ssoFetcherOrDefault().Fetch(ctx, issuerURL)
}

// ssoExchangeCode posts the code to the IdP's token endpoint.
type oidcTokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (s *Server) ssoExchangeCode(ctx context.Context, tokenURL string, sso *config.SSO, code string) (*oidcTokenResponse, error) {
	if tokenURL == "" {
		return nil, errors.New("discovery missing token_endpoint")
	}
	body := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {sso.RedirectURL},
		"client_id":    {sso.ClientID},
	}
	if sso.ClientSecret != "" {
		body.Set("client_secret", sso.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.ssoHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var t oidcTokenResponse
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &t, nil
}

// ssoIDTokenClaims is the subset of OIDC ID-token claims we use.
type ssoIDTokenClaims struct {
	Issuer            string         `json:"iss"`
	Subject           string         `json:"sub"`
	Audience          any            `json:"aud"` // string or []string
	Nonce             string         `json:"nonce"`
	Email             string         `json:"email"`
	PreferredUsername string         `json:"preferred_username"`
	Name              string         `json:"name"`
	Groups            []string       `json:"groups,omitempty"`
	Roles             []string       `json:"roles,omitempty"`
	Extra             map[string]any `json:"-"`
}

// ssoParseIDToken decodes & lightly validates the ID token.
//
// Why not full JWT signature verification here? The token came directly
// from the IdP over TLS to our token_endpoint request (no replay surface
// — we never expose this token to the browser). For defence-in-depth
// against a compromised IdP-side proxy we'd want JWKS verification; that
// belongs in internal/auth.JWTVerifier (already exists), wired here in a
// follow-up. The current implementation accepts what the IdP says
// because we already trust the IdP's TLS chain.
func (s *Server) ssoParseIDToken(idToken string, sso *config.SSO, disc auth.OIDCDiscovery) (*ssoIDTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	var claims ssoIDTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	// Extra map: re-parse to grab the raw map for GroupClaim lookups beyond
	// the named fields above (e.g. "https://example.com/roles").
	_ = json.Unmarshal(payload, &claims.Extra)
	if disc.Issuer != "" && claims.Issuer != "" && claims.Issuer != disc.Issuer {
		return nil, fmt.Errorf("issuer mismatch: got %q want %q", claims.Issuer, disc.Issuer)
	}
	if !audienceContains(claims.Audience, sso.ClientID) {
		return nil, fmt.Errorf("audience mismatch: client_id %q not in aud %v", sso.ClientID, claims.Audience)
	}
	return &claims, nil
}

func audienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == want {
				return true
			}
		}
	}
	return false
}

func pickSubject(c *ssoIDTokenClaims) string {
	if c.Email != "" {
		return c.Email
	}
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	return c.Subject
}

// mapRoles applies sso.RoleMapping + DefaultRole.
//
// Lookup keys come from sso.GroupClaim if non-default (otherwise we try
// "groups" + "roles" — both are common). Unmatched groups are dropped.
func mapRoles(c *ssoIDTokenClaims, sso *config.SSO) []string {
	candidates := []string{}
	if sso.GroupClaim != "" {
		if raw, ok := c.Extra[sso.GroupClaim]; ok {
			candidates = append(candidates, toStringSlice(raw)...)
		}
	} else {
		candidates = append(candidates, c.Groups...)
		candidates = append(candidates, c.Roles...)
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, g := range candidates {
		if mapped, ok := sso.RoleMapping[g]; ok {
			if _, dup := seen[mapped]; !dup {
				out = append(out, mapped)
				seen[mapped] = struct{}{}
			}
		}
	}
	if len(out) == 0 && sso.DefaultRole != "" {
		out = []string{sso.DefaultRole}
	}
	return out
}

func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	case string:
		return []string{x}
	}
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- helpers for lazy injection so tests can stub these out ---

func (s *Server) ssoFetcherOrDefault() *auth.OIDCFetcher {
	if s.OIDCFetcher != nil {
		return s.OIDCFetcher
	}
	s.OIDCFetcher = auth.NewOIDCFetcher()
	return s.OIDCFetcher
}

func (s *Server) ssoHTTPClient() *http.Client {
	if s.SSOHTTPClient != nil {
		return s.SSOHTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (s *Server) ssoStates() *ssoStateStore {
	if s.ssoStateStoreInst == nil {
		s.ssoStateStoreInst = newSSOStateStore()
	}
	return s.ssoStateStoreInst
}
