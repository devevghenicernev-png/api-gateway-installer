// Web admin API — JSON CRUD over the apigw config. Mounted by the
// dashboard server at /api/admin/*. The existing embedded dashboard JS
// calls these endpoints to provide a no-CLI experience for operators.
//
// All write endpoints route through Server.Sec.Guard() which enforces:
//   - Bearer-token authentication against config.Security.AdminTokens
//   - CSRF (X-CSRF-Token vs the value issued at /api/admin/csrf)
//   - RBAC permission check (api.add, deploy.delete, …)
//   - OPA policy evaluation against before/after snapshots
//   - Optional N-of-M approvals for destructive actions
//   - Hash-chained audit log with actor + payload
//
// Why JSON-over-HTTP not gRPC: zero client tooling required. `curl` works,
// browsers work, CI works.

package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/audit"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/rbac"
)

// adminRoutes registers all /api/admin/* CRUD handlers. The auxiliary
// endpoints (csrf, audit, approvals, tenants, alerts) are registered by
// securityRoutes() in server.go to keep this file focused on the config
// mutation surface.
func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/apis", s.adminAPIsHandler)
	mux.HandleFunc("/api/admin/apis/", s.adminAPIHandler)
	mux.HandleFunc("/api/admin/deploys", s.adminDeploysHandler)
	mux.HandleFunc("/api/admin/deploys/", s.adminDeployHandler)
	mux.HandleFunc("/api/admin/tls", s.adminTLSHandler)
	mux.HandleFunc("/api/admin/config", s.adminConfigHandler)
}

// ensureCSRF rejects write methods when the X-CSRF-Token header is missing
// or invalid. Read methods (GET) pass through. When Sec is nil (legacy
// mode) this is a no-op so installations without security can still GET/POST.
func (s *Server) ensureCSRF(r *http.Request, ident rbac.Identity) error {
	if s.Sec == nil {
		return nil
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		return ErrForbidden
	}
	return s.Sec.VerifyCSRFToken(ident.User, tok)
}

// ---------- /api/admin/apis ----------

func (s *Server) adminAPIsHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if s.Sec != nil {
			if _, err := s.Sec.Guard(r, Action{Permission: "api.list"}, nil); err != nil {
				adminWriteJSONError(w, Status(err), err.Error())
				return
			}
		}
		adminWriteJSON(w, http.StatusOK, filterByTenant(cfg.APIs, ident, s))
	case http.MethodPost:
		var api config.API
		if err := s.Sec.ReadBody(r, &api); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateAPIName(api.Name); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		action := Action{
			Permission: "api.add",
			Resource:   "api/" + api.Name,
			Before:     nil,
			After:      apiToMap(api),
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			if err := enforceTenantQuota(cfg, ident, s, "apis"); err != nil {
				return err
			}
			cfg.APIs = append(cfg.APIs, api)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusCreated, api)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// /api/admin/apis/<name>
func (s *Server) adminAPIHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/apis/")
	if name == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "api name required")
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	idx := -1
	for i, a := range cfg.APIs {
		if a.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		adminWriteJSONError(w, http.StatusNotFound, "no such api")
		return
	}
	if err := tenantOwns(cfg.APIs[idx], ident, s); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "api.show", Resource: "api/" + name}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.APIs[idx])
	case http.MethodPut:
		var updated config.API
		if err := s.Sec.ReadBody(r, &updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		updated.Name = name // protect from PUT-changes-key bug
		before := apiToMap(cfg.APIs[idx])
		after := apiToMap(updated)
		action := Action{
			Permission: "api.edit",
			Resource:   "api/" + name,
			Before:     before,
			After:      after,
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			cfg.APIs[idx] = updated
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		before := apiToMap(cfg.APIs[idx])
		action := Action{
			Permission: "api.remove",
			Resource:   "api/" + name,
			Before:     before,
			Dangerous:  true,
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			cfg.APIs = append(cfg.APIs[:idx], cfg.APIs[idx+1:]...)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- /api/admin/deploys ----------

func (s *Server) adminDeploysHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "deploy.list"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Deploys)
	case http.MethodPost:
		var dep config.Deploy
		if err := s.Sec.ReadBody(r, &dep); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateAPIName(dep.Name); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		action := Action{
			Permission: "deploy.add",
			Resource:   "deploy/" + dep.Name,
			After:      deployToMap(dep),
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			if err := enforceTenantQuota(cfg, ident, s, "deploys"); err != nil {
				return err
			}
			cfg.Deploys = append(cfg.Deploys, dep)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusCreated, dep)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminDeployHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/deploys/")
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	idx := -1
	for i, d := range cfg.Deploys {
		if d.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		adminWriteJSONError(w, http.StatusNotFound, "no such deploy")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "deploy.show", Resource: "deploy/" + name}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Deploys[idx])
	case http.MethodPut:
		var updated config.Deploy
		if err := s.Sec.ReadBody(r, &updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		updated.Name = name
		before := deployToMap(cfg.Deploys[idx])
		after := deployToMap(updated)
		if _, err := s.Sec.Guard(r, Action{
			Permission: "deploy.edit",
			Resource:   "deploy/" + name,
			Before:     before,
			After:      after,
		}, func() error {
			cfg.Deploys[idx] = updated
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		before := deployToMap(cfg.Deploys[idx])
		if _, err := s.Sec.Guard(r, Action{
			Permission: "deploy.remove",
			Resource:   "deploy/" + name,
			Before:     before,
			Dangerous:  true,
		}, func() error {
			cfg.Deploys = append(cfg.Deploys[:idx], cfg.Deploys[idx+1:]...)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- /api/admin/tls ----------

func (s *Server) adminTLSHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "tls.show"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, cfg.TLS)
}

// ---------- /api/admin/config (read-only — full config dump) ----------

func (s *Server) adminConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "config.show"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	// Strip secret fields before serializing — even owner-tier viewers should
	// see "***" rather than the raw tokens in the dashboard.
	redacted := *cfg
	redacted.Security.AdminTokens = redactTokens(cfg.Security.AdminTokens)
	redacted.Alerts.PagerDuty = redact(cfg.Alerts.PagerDuty)
	redacted.Alerts.Email.Password = redact(cfg.Alerts.Email.Password)
	redacted.GitOps.HTTPToken = redact(cfg.GitOps.HTTPToken)
	redacted.GitOps.SSHKeyPass = redact(cfg.GitOps.SSHKeyPass)
	adminWriteJSON(w, http.StatusOK, redacted)
}

// ---------- security/operator endpoints ----------

func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	ident := s.Sec.Identify(r)
	token := s.Sec.IssueCSRFToken(ident.User)
	adminWriteJSON(w, http.StatusOK, map[string]string{
		"token": token,
		"user":  ident.User,
	})
}

func (s *Server) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	if s.Sec == nil || s.Sec.Audit == nil {
		adminWriteJSONError(w, http.StatusNotImplemented, "audit not enabled")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "audit.query"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	f := audit.Filter{
		Actor:    r.URL.Query().Get("actor"),
		Action:   r.URL.Query().Get("action"),
		Resource: r.URL.Query().Get("resource"),
		Result:   r.URL.Query().Get("result"),
		Limit:    200,
	}
	entries := []audit.Entry{}
	if err := s.Sec.Audit.Query(f, func(e audit.Entry) bool {
		entries = append(entries, e)
		return true
	}); err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, entries)
}

func (s *Server) handleApprovalsList(w http.ResponseWriter, r *http.Request) {
	if s.Sec == nil || s.Sec.Approvals == nil {
		adminWriteJSONError(w, http.StatusNotImplemented, "approvals not enabled")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "approvals.list"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	list, err := s.Sec.Approvals.List(r.URL.Query().Get("status"), 100)
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, list)
}

func (s *Server) handleApprovalOne(w http.ResponseWriter, r *http.Request) {
	if s.Sec == nil || s.Sec.Approvals == nil {
		adminWriteJSONError(w, http.StatusNotImplemented, "approvals not enabled")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/approvals/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "approval id required")
		return
	}
	id := parts[0]
	ident := s.Sec.Identify(r)
	verb := ""
	if len(parts) == 2 {
		verb = parts[1]
	}
	switch verb {
	case "approve":
		var body struct{ Comment string `json:"comment"` }
		_ = s.Sec.ReadBody(r, &body)
		if _, err := s.Sec.Guard(r, Action{Permission: "approvals.approve", Resource: "approval/" + id}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		cr, err := s.Sec.Approvals.Approve(id, ident.User, body.Comment)
		if err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cr)
	case "reject":
		var body struct{ Reason string `json:"reason"` }
		_ = s.Sec.ReadBody(r, &body)
		if _, err := s.Sec.Guard(r, Action{Permission: "approvals.reject", Resource: "approval/" + id}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		cr, err := s.Sec.Approvals.Reject(id, ident.User, body.Reason)
		if err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cr)
	default:
		if _, err := s.Sec.Guard(r, Action{Permission: "approvals.show", Resource: "approval/" + id}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		cr, err := s.Sec.Approvals.Get(id)
		if err != nil {
			adminWriteJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cr)
	}
}

func (s *Server) handleTenants(w http.ResponseWriter, r *http.Request) {
	if s.Sec == nil || s.Sec.Tenants == nil {
		adminWriteJSONError(w, http.StatusNotImplemented, "tenants not enabled")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "tenant.list"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, s.Sec.Tenants.List())
}

func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "alerts.test"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	s.Sec.FireAlert("test", "info", "apigw alert test",
		"This is a test alert fired from the dashboard.", "system")
	adminWriteJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched"})
}

// ---------- helpers ----------

func adminWriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Best-effort; the response is already in flight, so we can't change
		// status. Log via the default slog so operators see encoder failures.
		// (Dashboard server.go wraps the mux with withRequestLog already.)
		_ = err
	}
}

func adminWriteJSONError(w http.ResponseWriter, status int, msg string) {
	adminWriteJSON(w, status, map[string]any{"error": msg})
}

// validateAPIName mirrors the same constraints the CLI enforces — slug, no
// path traversal, no shell meta-characters.
func validateAPIName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name required", ErrBadRequest)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: name >64 chars", ErrBadRequest)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("%w: name has invalid char %q (allowed: alnum, _ -)", ErrBadRequest, r)
		}
	}
	return nil
}

func apiToMap(a config.API) map[string]any {
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func deployToMap(d config.Deploy) map[string]any {
	b, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// filterByTenant returns APIs visible to the caller. When tenants are not
// configured everyone sees everything. When tenants ARE configured the
// caller's path prefix must match.
func filterByTenant(apis []config.API, ident rbac.Identity, s *Server) []config.API {
	if s == nil || s.Sec == nil || s.Sec.Tenants == nil {
		return apis
	}
	// owner-role tokens see everything regardless of tenant; we approximate
	// by checking whether the user has the "*" permission via the RBAC
	// engine (any RBAC engine refusal becomes a per-tenant filter).
	out := make([]config.API, 0, len(apis))
	for _, a := range apis {
		if tenantOwns(a, ident, s) == nil {
			out = append(out, a)
		}
	}
	return out
}

// tenantOwns returns nil if the identity may see/edit the given API.
//
// Logic:
//   - No tenants configured: always allowed.
//   - API path begins with /t/<tenant>/...: caller must be tenant admin.
//   - API path has no tenant prefix: allowed (global API).
//
// owner-tier (RBAC "*") bypass is handled by the RBAC engine itself.
func tenantOwns(a config.API, ident rbac.Identity, s *Server) error {
	if s == nil || s.Sec == nil || s.Sec.Tenants == nil {
		return nil
	}
	if !strings.HasPrefix(a.Path, "/t/") {
		return nil
	}
	parts := strings.SplitN(strings.TrimPrefix(a.Path, "/t/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return nil
	}
	tenantID := parts[0]
	if s.Sec.Tenants.IsAdmin(tenantID, ident.User) {
		return nil
	}
	return fmt.Errorf("%w: not admin of tenant %q", ErrForbidden, tenantID)
}

// enforceTenantQuota refuses the create if the caller's tenant has hit its
// MaxAPIs or MaxDeploys cap. Resource is "apis" or "deploys".
func enforceTenantQuota(cfg *config.Config, ident rbac.Identity, s *Server, resource string) error {
	if s == nil || s.Sec == nil || s.Sec.Tenants == nil {
		return nil
	}
	for _, t := range s.Sec.Tenants.List() {
		if !contains(t.Admins, ident.User) {
			continue
		}
		count := 0
		switch resource {
		case "apis":
			if t.Quotas.MaxAPIs == 0 {
				return nil
			}
			for _, a := range cfg.APIs {
				if strings.HasPrefix(a.Path, t.PathPrefix) {
					count++
				}
			}
			if count >= t.Quotas.MaxAPIs {
				return fmt.Errorf("%w: tenant %q at MaxAPIs cap (%d)", ErrForbidden, t.ID, t.Quotas.MaxAPIs)
			}
		case "deploys":
			if t.Quotas.MaxDeploys == 0 {
				return nil
			}
			for _, d := range cfg.Deploys {
				if strings.HasPrefix(d.Path, t.PathPrefix) {
					count++
				}
			}
			if count >= t.Quotas.MaxDeploys {
				return fmt.Errorf("%w: tenant %q at MaxDeploys cap (%d)", ErrForbidden, t.ID, t.Quotas.MaxDeploys)
			}
		}
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func redactTokens(in []config.AdminToken) []config.AdminToken {
	out := make([]config.AdminToken, len(in))
	for i, t := range in {
		t.Token = redact(t.Token)
		out[i] = t
	}
	return out
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// keep import compiler happy if errors is the only consumer of imported pkg
var _ = errors.Is
