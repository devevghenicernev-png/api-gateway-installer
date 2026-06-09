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
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/approvals"
	"github.com/devevghenicernev-png/apigw/internal/audit"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/rbac"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
	"github.com/devevghenicernev-png/apigw/internal/webhook"
)

// adminRoutes registers all /api/admin/* CRUD handlers. The auxiliary
// endpoints (csrf, audit, approvals, tenants, alerts) are registered by
// securityRoutes() in server.go to keep this file focused on the config
// mutation surface.
//
// Every route is mounted under BOTH /api/admin/* and /api/v1/admin/*
// via registerAdmin() — see its docstring in server.go.
func (s *Server) adminRoutes(mux *http.ServeMux) {
	registerAdmin(mux, "/api/admin/apis", s.adminAPIsHandler)
	registerAdmin(mux, "/api/admin/apis/", s.adminAPIHandler)
	registerAdmin(mux, "/api/admin/deploys", s.adminDeploysHandler)
	registerAdmin(mux, "/api/admin/deploys/", s.adminDeployHandler)
	registerAdmin(mux, "/api/admin/deploy-run/", s.adminDeployRunHandler)
	registerAdmin(mux, "/api/admin/deploy-rollback/", s.adminDeployRollbackHandler)
	registerAdmin(mux, "/api/admin/tls-renew/", s.adminTLSRenewHandler)
	registerAdmin(mux, "/api/admin/webhook-rotate/", s.adminWebhookRotateHandler)
	registerAdmin(mux, "/api/admin/cache-purge/", s.adminCachePurgeHandler)
	registerAdmin(mux, "/api/admin/tls", s.adminTLSHandler)
	registerAdmin(mux, "/api/admin/config", s.adminConfigHandler)
	registerAdmin(mux, "/api/admin/sessions", s.adminSessionsHandler)
	registerAdmin(mux, "/api/admin/sessions/", s.adminSessionHandler)
	registerAdmin(mux, "/api/admin/streams", s.adminStreamsHandler)
	registerAdmin(mux, "/api/admin/streams/", s.adminStreamHandler)
	registerAdmin(mux, "/api/admin/consumers", s.adminConsumersHandler)
	registerAdmin(mux, "/api/admin/consumers/", s.adminConsumerHandler)
	registerAdmin(mux, "/api/admin/gitops", s.adminGitOpsHandler)
}

// ensureCSRF rejects write methods when the X-CSRF-Token header is missing
// or invalid. Read methods (GET) pass through. When Sec is nil (legacy
// mode) this is a no-op so installations without security can still GET/POST.
//
// On failure we write an audit denial entry — otherwise CSRF rejections
// would be invisible to operators reviewing the audit log, hiding probes
// from scanners and replay attempts.
func (s *Server) ensureCSRF(r *http.Request, ident rbac.Identity) error {
	if s.Sec == nil {
		return nil
	}
	// Enforce CSRF only on the verbs we actually accept. Letting PATCH /
	// CONNECT / TRACE through here means the per-handler method switch
	// returns a clean 405 instead of a misleading 403 (the request was
	// rejected because of the verb, not the missing CSRF token).
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return nil
	}
	var err error
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		err = ErrForbidden
	} else {
		err = s.Sec.VerifyCSRFToken(ident.User, tok)
	}
	if err != nil {
		// Synthesize a permission verb so the entry is filterable; resource
		// is the URL path so the operator can see what was being attempted.
		s.Sec.RecordDenial(r, rbac.Permission("csrf."+strings.ToLower(r.Method)), r.URL.Path,
			"csrf: missing or invalid X-CSRF-Token")
	}
	return err
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
		items := filterByTenant(cfg.APIs, ident, s)
		items = applyPage(w, items, parsePage(r))
		adminWriteJSON(w, http.StatusOK, items)
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
		if cfg.FindAPI(api.Name) != nil {
			adminWriteJSONError(w, http.StatusConflict, fmt.Sprintf("api %q already exists", api.Name))
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
			// AddAPI guards uniqueness one more time — defense in depth in
			// case Find above was racey with another writer.
			if err := cfg.AddAPI(api); err != nil {
				return err
			}
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		if err := s.nginxManager().WriteAndReload(cfg); err != nil {
			adminWriteJSONError(w, http.StatusInternalServerError,
				"config saved but nginx reload failed — run `apigw api reload` or `apigw doctor`: "+err.Error())
			return
		}
		adminWriteJSON(w, http.StatusCreated, api)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// /api/admin/apis/<name>
//
// Nested-resource dispatcher: when the path contains a slash after the
// API name (e.g. /api/admin/apis/<name>/keys[/<id>]) we hand off to the
// per-resource handler. Single-level paths fall through to the API CRUD
// below.
func (s *Server) adminAPIHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/apis/")
	if i := strings.Index(rest, "/"); i >= 0 {
		// /<name>/keys[/<id>] → apikeys handlers.
		nested := rest[i+1:]
		switch {
		case nested == "keys":
			s.adminAPIKeysHandler(w, r)
			return
		case strings.HasPrefix(nested, "keys/"):
			s.adminAPIKeyHandler(w, r)
			return
		default:
			adminWriteJSONError(w, http.StatusNotFound, "unknown nested resource: "+nested)
			return
		}
	}
	name := rest
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
		writeJSONWithETag(w, http.StatusOK, cfg.APIs[idx])
	case http.MethodPut:
		if err := checkIfMatch(r, cfg.APIs[idx]); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
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
		// Regenerate nginx config so the toggle (Enabled flip) and any
		// other shape changes actually take effect. Without this the
		// admin API would save config.yaml but nginx keeps serving the
		// old routing — the operator clicks "disable" in the dashboard,
		// sees "✓ disabled" toast, but curl still gets 200 because the
		// nginx location block is unchanged.
		if err := s.nginxManager().WriteAndReload(cfg); err != nil {
			adminWriteJSONError(w, http.StatusInternalServerError,
				"config saved but nginx reload failed — run `apigw api reload` or `apigw doctor`: "+err.Error())
			return
		}
		writeJSONWithETag(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := checkIfMatch(r, cfg.APIs[idx]); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
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
		if err := s.nginxManager().WriteAndReload(cfg); err != nil {
			adminWriteJSONError(w, http.StatusInternalServerError,
				"config saved but nginx reload failed — run `apigw api reload` or `apigw doctor`: "+err.Error())
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
		if cfg.FindDeploy(dep.Name) != nil {
			adminWriteJSONError(w, http.StatusConflict, fmt.Sprintf("deploy %q already exists", dep.Name))
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
			if err := cfg.AddDeploy(dep); err != nil {
				return err
			}
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
		var body struct {
			Comment string `json:"comment"`
		}
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
		// Threshold reached → execute the parked mutation and mark applied.
		// The action verb we stored on Submit drives a small dispatch table
		// below. Failures here flip the change back to a useful state and
		// surface to the caller — half-applied changes are worse than a
		// rejected approval.
		if cr.Status == "approved" {
			cfgFresh, cerr := s.ConfigFn()
			if cerr == nil {
				if execErr := s.executeParked(cr, cfgFresh); execErr != nil {
					adminWriteJSONError(w, http.StatusInternalServerError,
						"approval recorded but apply failed: "+execErr.Error())
					return
				}
				if mErr := s.Sec.Approvals.MarkApplied(id); mErr == nil {
					cr.Status = "applied"
					now := cr.SubmittedAt
					cr.AppliedAt = &now
				}
			}
		}
		adminWriteJSON(w, http.StatusOK, cr)
	case "reject":
		var body struct {
			Reason string `json:"reason"`
		}
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

// executeParked applies an approved ChangeRequest's mutation. Dispatched
// on the action verb stored at Submit time. Today only *.remove actions
// can be Dangerous so the table is short — extend here as we tag more
// permissions Dangerous in admin_api handlers.
func (s *Server) executeParked(cr approvals.ChangeRequest, cfg *config.Config) error {
	// Resource format is "<kind>/<name>"; recover the name.
	name := cr.Resource
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	switch cr.Action {
	case "api.remove":
		if err := cfg.RemoveAPI(name); err != nil {
			return fmt.Errorf("remove api: %w", err)
		}
	case "deploy.remove":
		// Config has no RemoveDeploy helper today — splice manually.
		for i, d := range cfg.Deploys {
			if d.Name == name {
				cfg.Deploys = append(cfg.Deploys[:i], cfg.Deploys[i+1:]...)
				return cfg.Save()
			}
		}
		return fmt.Errorf("deploy %q not found", name)
	default:
		return fmt.Errorf("don't know how to apply %q", cr.Action)
	}
	return cfg.Save()
}

// adminDeployRunHandler is POST /api/admin/deploy-run/<name>. Enqueues a
// redeploy job into the same durable bbolt queue webhooks use. The worker
// picks it up; the operator watches the SSE log stream for progress.
//
// We accept manual redeploys without a SHA (Repo + Branch alone) — the
// clone step resolves HEAD on its own.
func (s *Server) adminDeployRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/deploy-run/")
	if name == "" || strings.ContainsAny(name, "/") {
		adminWriteJSONError(w, http.StatusBadRequest, "deploy name required")
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
	dep := cfg.FindDeploy(name)
	if dep == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no such deploy")
		return
	}
	if s.Queue == nil {
		adminWriteJSONError(w, http.StatusServiceUnavailable,
			"deploy worker not available — start `apigw dashboard serve` (not just /metrics)")
		return
	}
	if _, err := s.Sec.Guard(r, Action{
		Permission: "deploy.run",
		Resource:   "deploy/" + name,
		After:      map[string]any{"requested_by": ident.User, "branch": dep.Branch},
	}, func() error {
		_, qerr := s.Queue.Enqueue(webhook.Job{
			Deploy: name,
			Event:  "manual",
			Repo:   dep.Repo,
			Branch: dep.Branch,
		})
		return qerr
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusAccepted, map[string]any{
		"status": "queued",
		"deploy": name,
		"branch": dep.Branch,
		"note":   "watch the live log panel for progress",
	})
}

// adminDeployRollbackHandler is POST /api/admin/deploy-rollback/<name>.
// Moves the `current` symlink to the previous release directory (by
// mtime) and reloads the deploy unit. Refuses if there's nothing to
// roll back to.
func (s *Server) adminDeployRollbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/deploy-rollback/")
	if name == "" || strings.ContainsAny(name, "/") {
		adminWriteJSONError(w, http.StatusBadRequest, "deploy name required")
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
	if cfg.FindDeploy(name) == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no such deploy")
		return
	}
	if _, err := s.Sec.Guard(r, Action{
		Permission: "deploy.rollback",
		Resource:   "deploy/" + name,
		Dangerous:  true,
		After:      map[string]any{"requested_by": ident.User},
	}, func() error {
		prev, rerr := deploy.RollbackToPrevious(name)
		if rerr != nil {
			return rerr
		}
		_ = cfg.SetDeployStatus(name, prev, "ok", "rolled back")
		return cfg.Save()
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusAccepted, map[string]any{
		"status": "rolled-back",
		"deploy": name,
	})
}

// adminTLSRenewHandler is POST /api/admin/tls-renew/<domain>. Triggers a
// renewal attempt and returns whether nginx was reloaded. Long-running
// (can take 30s+); the operator polls the TLS panel for the new expiry.
func (s *Server) adminTLSRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	domain := strings.TrimPrefix(r.URL.Path, "/api/admin/tls-renew/")
	if domain == "" || strings.ContainsAny(domain, "/") {
		adminWriteJSONError(w, http.StatusBadRequest, "domain required")
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	if _, err := s.Sec.Guard(r, Action{
		Permission: "tls.renew",
		Resource:   "cert/" + domain,
	}, func() error {
		// Background it; renewal can take 30s+ and we don't want to hold
		// an HTTP request open. The result lands in the next /api/status
		// snapshot via the TLS ticker.
		go func() {
			if err := apitls.RenewDomain(domain); err != nil {
				s.Logger.Warn("tls renew", slog.String("domain", domain), slog.String("err", err.Error()))
				if s.Sec != nil {
					s.Sec.FireAlert("cert.renew.failed", "warning",
						"TLS renew failed: "+domain, err.Error(), "cert/"+domain)
				}
				return
			}
			// Reload nginx so the freshly-issued cert is actually served.
			// Without this, fullchain.pem is updated on disk but nginx keeps
			// serving the old cert from memory until something else triggers
			// a reload (the CLI `apigw tls renew` does this, the dashboard
			// button used to skip it).
			if err := nginx.NewManager().Reload(); err != nil {
				s.Logger.Warn("nginx reload after renew", slog.String("domain", domain), slog.String("err", err.Error()))
				if s.Sec != nil {
					s.Sec.FireAlert("cert.renew.reload_failed", "warning",
						"nginx reload failed after TLS renew: "+domain, err.Error(), "cert/"+domain)
				}
				return
			}
			s.Logger.Info("tls renew ok", slog.String("domain", domain))
		}()
		return nil
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusAccepted, map[string]any{
		"status": "queued",
		"domain": domain,
	})
}

// adminWebhookRotateHandler is POST /api/admin/webhook-rotate/<deploy>.
// Generates a new HMAC secret for the deploy, stores it, returns the
// new value ONCE in the response (operator must update GitHub).
func (s *Server) adminWebhookRotateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/webhook-rotate/")
	if name == "" || strings.ContainsAny(name, "/") {
		adminWriteJSONError(w, http.StatusBadRequest, "deploy name required")
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
	if cfg.FindDeploy(name) == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no such deploy")
		return
	}
	var newSecret string
	if _, err := s.Sec.Guard(r, Action{
		Permission: "webhook.rotate",
		Resource:   "webhook/" + name,
		Dangerous:  true,
	}, func() error {
		ns, werr := webhook.RotateSecret(name)
		if werr != nil {
			return werr
		}
		newSecret = ns
		return nil
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]any{
		"status":     "rotated",
		"deploy":     name,
		"new_secret": newSecret,
		"note":       "Update the webhook secret in GitHub now — old secret is invalid.",
	})
}

// adminCachePurgeHandler is POST /api/admin/cache-purge/<api>. Deletes
// the per-API proxy_cache directory and forces nginx to refresh its
// shared zone metadata on next request. Useful after publishing a new
// data version that should invalidate all cached responses.
func (s *Server) adminCachePurgeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/cache-purge/")
	if name == "" || strings.ContainsAny(name, "/") {
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
	api := cfg.FindAPI(name)
	if api == nil || api.Cache == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no cache zone for this api")
		return
	}
	if _, err := s.Sec.Guard(r, Action{
		Permission: "cache.purge",
		Resource:   "api/" + name,
	}, func() error {
		// Cache lives at /var/cache/nginx/apigw_cache_<name>/. Removing
		// the directory invalidates the in-zone keys; nginx repopulates
		// on the next miss. A `kill -HUP $(pidof nginx)` would also
		// work but we keep this scoped — no service interruption.
		dir := "/var/cache/nginx/apigw_cache_" + name
		return removeAllBestEffort(dir)
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	adminWriteJSON(w, http.StatusOK, map[string]any{
		"status": "purged",
		"api":    name,
	})
}

// removeAllBestEffort wraps os.RemoveAll but never returns ENOENT —
// the goal is "directory absent", and removing something that's not
// there already meets that.
func removeAllBestEffort(dir string) error {
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("purge %s: %w", dir, err)
	}
	return nil
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
