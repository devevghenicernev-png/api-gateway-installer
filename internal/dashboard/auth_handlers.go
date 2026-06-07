// Auth-request handlers for non-JWT auth types: API key, OAuth 2.0
// introspection, and the Mock-response shortcut. nginx calls these via
// `auth_request /auth/<kind>/<api>` (or for mocks, the location's
// proxy_pass directly). All three return only status codes — bodies
// are ignored by nginx auth_request — except the mock handler which
// renders a canned response.
package dashboard

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/auth"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

// oauth2Verifiers caches verifiers keyed by api name so we don't rebuild
// (and re-allocate the cache map) on every request. Rebuilt on config
// reload via the security file-watcher.
var oauth2Verifiers = newVerifierCache()

type verifierCache struct {
	v map[string]*auth.OAuth2Verifier
}

func newVerifierCache() *verifierCache {
	return &verifierCache{v: map[string]*auth.OAuth2Verifier{}}
}

func (c *verifierCache) get(name string, cfg auth.OAuth2Config) (*auth.OAuth2Verifier, error) {
	if v, ok := c.v[name]; ok {
		return v, nil
	}
	v, err := auth.NewOAuth2Verifier(cfg)
	if err != nil {
		return nil, err
	}
	c.v[name] = v
	return v, nil
}

func (s *Server) handleAPIKeyAuth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/apikey/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		s.Logger.Error("apikey: load config", "err", err)
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
	if apiCfg == nil || apiCfg.APIKey == nil {
		s.Logger.Warn("apikey: no config for api", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	keys := make([]auth.APIKeyEntry, len(apiCfg.APIKey.Keys))
	for i, k := range apiCfg.APIKey.Keys {
		keys[i] = auth.APIKeyEntry{
			ID: k.ID, Secret: k.Secret, Owner: k.Owner,
			RPS: k.RPS, ExpiresAt: k.ExpiresAt, Disabled: k.Disabled,
			Scopes: k.Scopes,
		}
	}
	verifier := auth.APIKeyConfig{
		Header:     apiCfg.APIKey.Header,
		QueryParam: apiCfg.APIKey.QueryParam,
		Keys:       keys,
	}
	res, entry, why := auth.VerifyAPIKey(verifier, r.Header, r.URL.Query())
	if res != auth.APIKeyOK {
		s.Logger.Info("apikey: reject", "api", apiName, "result", int(res), "reason", why)
		w.WriteHeader(res.HTTPStatus())
		return
	}
	// Surface identity for the upstream via headers auth_request_set
	// can copy forward. Useful for app-level audit + per-user analytics.
	if entry != nil {
		w.Header().Set("X-Apigw-ApiKey-ID", entry.ID)
		if entry.Owner != "" {
			w.Header().Set("X-Apigw-ApiKey-Owner", entry.Owner)
		}
		if len(entry.Scopes) > 0 {
			w.Header().Set("X-Apigw-ApiKey-Scopes", strings.Join(entry.Scopes, " "))
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleHMACAuth backs nginx auth_request /auth/hmac/<api>. The
// subrequest is always GET on /auth/hmac/<api>, so the original method
// and URI are passed via X-Original-Method and X-Original-URI headers
// (set in the _locations.tmpl HMAC block).
func (s *Server) handleHMACAuth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/hmac/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		s.Logger.Error("hmac: load config", "err", err)
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
	if apiCfg == nil || apiCfg.HMAC == nil {
		s.Logger.Warn("hmac: no config for api", "api", apiName)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	keys := make([]auth.HMACKeyEntry, len(apiCfg.HMAC.Keys))
	for i, k := range apiCfg.HMAC.Keys {
		keys[i] = auth.HMACKeyEntry{
			ID:        k.ID,
			Secret:    k.Secret,
			Algorithm: k.Algorithm,
			Owner:     k.Owner,
			ExpiresAt: k.ExpiresAt,
			Disabled:  k.Disabled,
			Scopes:    k.Scopes,
		}
	}
	verifier := auth.HMACConfig{
		Algorithms:      apiCfg.HMAC.Algorithms,
		ClockSkew:       apiCfg.HMAC.ClockSkew,
		RequireBodyHash: apiCfg.HMAC.RequireBodyHash,
		NonceCacheSize:  apiCfg.HMAC.NonceCacheSize,
		Keys:            keys,
	}

	method := r.Header.Get("X-Original-Method")
	if method == "" {
		method = "GET"
	}
	var path, query string
	if rawURI := r.Header.Get("X-Original-URI"); rawURI != "" {
		if u, perr := url.ParseRequestURI(rawURI); perr == nil {
			path = u.Path
			query = u.RawQuery
		} else {
			// fall back: best-effort split
			if i := strings.IndexByte(rawURI, '?'); i >= 0 {
				path, query = rawURI[:i], rawURI[i+1:]
			} else {
				path = rawURI
			}
		}
	}

	hreq := auth.HMACRequest{
		Method:        method,
		Path:          path,
		Query:         query,
		Authorization: r.Header.Get("Authorization"),
		Date:          r.Header.Get("X-Apigw-Date"),
		Nonce:         r.Header.Get("X-Apigw-Nonce"),
		ContentSHA256: r.Header.Get("X-Apigw-Content-SHA256"),
	}
	nonces := s.hmacNonces.For(apiName, apiCfg.HMAC.NonceCacheSize)
	res, entry, why := auth.VerifyHMAC(verifier, hreq, nonces)
	if res != auth.HMACOK {
		s.Logger.Info("hmac: reject", "api", apiName, "result", int(res), "reason", why)
		w.WriteHeader(res.HTTPStatus())
		return
	}
	if entry != nil {
		w.Header().Set("X-Apigw-HMAC-Key-ID", entry.ID)
		if entry.Owner != "" {
			w.Header().Set("X-Apigw-HMAC-Owner", entry.Owner)
		}
		if len(entry.Scopes) > 0 {
			w.Header().Set("X-Apigw-HMAC-Scopes", strings.Join(entry.Scopes, " "))
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleOAuth2Auth(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/auth/oauth2/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
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
	if apiCfg == nil || apiCfg.OAuth2 == nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	verifier, err := oauth2Verifiers.get(apiName, auth.OAuth2Config{
		IntrospectionURL: apiCfg.OAuth2.IntrospectionURL,
		ClientID:         apiCfg.OAuth2.ClientID,
		ClientSecret:     apiCfg.OAuth2.ClientSecret,
		RequiredScopes:   apiCfg.OAuth2.RequiredScopes,
		CacheSeconds:     apiCfg.OAuth2.CacheSeconds,
	})
	if err != nil {
		s.Logger.Error("oauth2 verifier", "err", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	token := r.Header.Get("Authorization")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	res, ir, why := verifier.VerifyToken(ctx, token)
	if res != auth.OK {
		s.Logger.Info("oauth2: reject", "api", apiName, "result", int(res), "reason", why)
		w.WriteHeader(res.HTTPStatus())
		return
	}
	if ir != nil {
		if ir.Subject != "" {
			w.Header().Set("X-Apigw-Subject", ir.Subject)
		}
		if ir.Username != "" {
			w.Header().Set("X-Apigw-Username", ir.Username)
		}
		if ir.Scope != "" {
			w.Header().Set("X-Apigw-Scopes", ir.Scope)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handleMock renders an API.Mock canned response. Called from nginx
// when the route's Mock block is set — the location skips proxy_pass
// entirely and routes straight here. The optional DelayMS lets dev
// teams simulate slow upstreams.
func (s *Server) handleMock(w http.ResponseWriter, r *http.Request) {
	apiName := strings.TrimPrefix(r.URL.Path, "/mock/")
	apiName = strings.TrimSuffix(apiName, "/")
	if apiName == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
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
	if apiCfg == nil || apiCfg.Mock == nil {
		http.NotFound(w, r)
		return
	}
	if apiCfg.Mock.DelayMS > 0 {
		time.Sleep(time.Duration(apiCfg.Mock.DelayMS) * time.Millisecond)
	}
	for k, v := range apiCfg.Mock.Headers {
		w.Header().Set(k, v)
	}
	status := apiCfg.Mock.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if apiCfg.Mock.Body != "" {
		_, _ = w.Write([]byte(apiCfg.Mock.Body))
	}
	_ = strconv.Itoa // keep import alive if no other use
}
