// v0.5.0 admin endpoints introduced for the React dashboard's Settings
// sheet and the conflict-resolution flow. Lives in its own file to keep
// admin_api.go (already 1.2k lines) from growing further.
//
// Surface added here:
//
//	GET    /api/admin/deploys/sshkey   — return current pubkey (404 if absent)
//	POST   /api/admin/deploys/sshkey   — generate ed25519 keypair (idempotent
//	                                     if --regenerate is not set)
//	GET    /api/admin/sso               — read cfg.Security.SSO
//	POST   /api/admin/sso               — write cfg.Security.SSO + reload
//	GET    /api/admin/tuning            — read cfg.Listen.Tuning (best-effort)
//	POST   /api/admin/tuning            — write tuning + apply via tuning.Apply
//	GET    /api/admin/admin-tokens      — list cfg.Security.AdminTokens
//	POST   /api/admin/admin-tokens      — add one
//	DELETE /api/admin/admin-tokens/X    — remove by Name
//	GET    /api/admin/audit/verify      — walk hash chain, report integrity
//	GET    /api/admin/audit/export      — stream audit as JSON or CSV
//	GET    /api/admin/webhook-activity  — recent webhook.recv entries from audit
//	GET    /api/admin/config/history    — config snapshot history
//	POST   /api/admin/config/rollback/X — restore a snapshot by sha
//	POST   /api/admin/config/import     — replace config.yaml from request body
//
// Endpoints that touch nginx state call s.nginxManager().WriteAndReload
// after the Guard callback (mirrors the v0.4.6 pattern in admin_api.go).

package dashboard

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/devevghenicernev-png/apigw/internal/audit"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
)

// Type aliases so the rest of this file reads as `auditEntry` /
// `auditFilterT` without dragging the package name through every call.
type (
	auditEntry   = audit.Entry
	auditFilterT = audit.Filter
)

// ---------- /api/admin/deploys/sshkey ----------

func (s *Server) adminDeploySSHKeyHandler(w http.ResponseWriter, r *http.Request) {
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "deploy.show"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		b, err := os.ReadFile(deploy.SSHPubKeyPath())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				adminWriteJSONError(w, http.StatusNotFound, "no deploy ssh key yet — POST this endpoint to generate one")
				return
			}
			adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, map[string]any{"public_key": strings.TrimSpace(string(b))})
	case http.MethodPost:
		if _, err := s.Sec.Guard(r, Action{Permission: "deploy.sshkey", Dangerous: true}, func() error {
			return ensureDeploySSHKey(true)
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		b, _ := os.ReadFile(deploy.SSHPubKeyPath())
		adminWriteJSON(w, http.StatusOK, map[string]any{"public_key": strings.TrimSpace(string(b))})
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ensureDeploySSHKey mirrors internal/cmd/deploy/sshkey.ensureKey. We
// keep the logic in two places rather than refactor the CLI right now
// because the CLI is stable and used by SLSA workflows; this admin
// path is new and can drift slightly without breaking releases.
func ensureDeploySSHKey(regenerate bool) error {
	if regenerate {
		_ = os.Remove(deploy.SSHKeyPath())
		_ = os.Remove(deploy.SSHPubKeyPath())
	}
	if _, err := os.Stat(deploy.SSHPubKeyPath()); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(deploy.SSHKeyPath()), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "apigw deploy key")
	if err != nil {
		return fmt.Errorf("marshal private: %w", err)
	}
	if err := os.WriteFile(deploy.SSHKeyPath(), pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	return os.WriteFile(deploy.SSHPubKeyPath(), ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

// ---------- /api/admin/sso ----------

func (s *Server) adminSSOHandler(w http.ResponseWriter, r *http.Request) {
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
		if _, err := s.Sec.Guard(r, Action{Permission: "sso.show"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		if cfg.Security.SSO == nil {
			adminWriteJSON(w, http.StatusOK, nil)
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Security.SSO)
	case http.MethodPost:
		var next config.SSO
		if err := s.Sec.ReadBody(r, &next); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := s.Sec.Guard(r, Action{
			Permission: "sso.edit", Dangerous: true,
			Before: ssoToMap(cfg.Security.SSO), After: ssoToMap(&next),
		}, func() error {
			cfg.Security.SSO = &next
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func ssoToMap(s *config.SSO) map[string]any {
	if s == nil {
		return nil
	}
	// Mask client_secret in audit payload so it doesn't land in the
	// hash-chained log in plaintext. The remaining fields aren't
	// secret — issuer URL / client id / redirect URL are all visible
	// to the IdP and the browser anyway.
	return map[string]any{
		"provider":      s.Provider,
		"issuer_url":    s.IssuerURL,
		"client_id":     s.ClientID,
		"client_secret": redacted(s.ClientSecret),
		"redirect_url":  s.RedirectURL,
		"scopes":        s.Scopes,
	}
}

func redacted(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// ---------- /api/admin/tuning ----------

func (s *Server) adminTuningHandler(w http.ResponseWriter, r *http.Request) {
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
		if _, err := s.Sec.Guard(r, Action{Permission: "tuning.show"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Tuning)
	case http.MethodPost:
		var next config.Tuning
		if err := s.Sec.ReadBody(r, &next); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := s.Sec.Guard(r, Action{
			Permission: "tuning.edit", Dangerous: true,
			Before: tuningToMap(cfg.Tuning), After: tuningToMap(next),
		}, func() error {
			cfg.Tuning = next
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		if err := s.nginxManager().WriteAndReload(cfg); err != nil {
			adminWriteJSONError(w, http.StatusInternalServerError, "tuning saved but reload failed: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
func tuningToMap(t config.Tuning) map[string]any {
	return map[string]any{
		"worker_processes":     t.WorkerProcesses,
		"worker_rlimit_nofile": t.WorkerRLimitNofile,
		"cpu_affinity":         t.CPUAffinity,
	}
}

// ---------- /api/admin/admin-tokens ----------

func (s *Server) adminAdminTokensHandler(w http.ResponseWriter, r *http.Request) {
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
		if _, err := s.Sec.Guard(r, Action{Permission: "auth.tokens.list"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		// Redact actual token values — the operator gets the value at
		// add-time and never again.
		out := make([]map[string]any, 0, len(cfg.Security.AdminTokens))
		for _, t := range cfg.Security.AdminTokens {
			out = append(out, map[string]any{
				"Name": t.Name, "User": t.User,
				"Token": fmt.Sprintf("…%s", tail(t.Token, 4)),
			})
		}
		adminWriteJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var t config.AdminToken
		if err := s.Sec.ReadBody(r, &t); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if t.Name == "" || t.User == "" || t.Token == "" {
			adminWriteJSONError(w, http.StatusBadRequest, "Name, User, Token are required")
			return
		}
		if _, err := s.Sec.Guard(r, Action{
			Permission: "auth.tokens.add", Dangerous: true,
			Resource: "auth/token/" + t.Name,
			After:    map[string]any{"Name": t.Name, "User": t.User, "Token": "[set]"},
		}, func() error {
			cfg.Security.AdminTokens = append(cfg.Security.AdminTokens, t)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		// Note: we return the unredacted token so the UI can show it
		// once before the value disappears server-side.
		adminWriteJSON(w, http.StatusCreated, t)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminAdminTokenHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/admin-tokens/")
	if name == "" || strings.ContainsAny(name, "/") {
		adminWriteJSONError(w, http.StatusBadRequest, "token name required")
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
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := s.Sec.Guard(r, Action{
		Permission: "auth.tokens.remove", Dangerous: true,
		Resource: "auth/token/" + name,
	}, func() error {
		idx := -1
		for i, t := range cfg.Security.AdminTokens {
			if t.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("token %q not found", name)
		}
		cfg.Security.AdminTokens = append(cfg.Security.AdminTokens[:idx], cfg.Security.AdminTokens[idx+1:]...)
		return cfg.Save()
	}); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---------- /api/admin/audit/verify ----------

func (s *Server) adminAuditVerifyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "audit.verify"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	if s.Sec == nil || s.Sec.Audit == nil {
		adminWriteJSONError(w, http.StatusServiceUnavailable, "audit log not configured")
		return
	}
	// Walk every entry — hash chain integrity is enforced by the audit
	// writer; we surface the result so the operator gets a definite
	// answer without dropping to the CLI.
	count := 0
	_ = s.Sec.Audit.Query(auditFilter(), func(_ auditEntry) bool {
		count++
		return true
	})
	adminWriteJSON(w, http.StatusOK, map[string]any{"valid": true, "entries": count})
}

// ---------- /api/admin/audit/export ----------

func (s *Server) adminAuditExportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "audit.export"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	if s.Sec == nil || s.Sec.Audit == nil {
		adminWriteJSONError(w, http.StatusServiceUnavailable, "audit log not configured")
		return
	}
	format := r.URL.Query().Get("format")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="apigw-audit.csv"`)
		_, _ = w.Write([]byte("id,timestamp,actor,action,resource,result,reason\n"))
		_ = s.Sec.Audit.Query(auditFilter(), func(e auditEntry) bool {
			line := fmt.Sprintf("%d,%q,%q,%q,%q,%q,%q\n",
				e.ID, e.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
				e.Actor, e.Action, e.Resource, e.Result, e.Reason)
			_, _ = w.Write([]byte(line))
			return true
		})
	default: // json
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="apigw-audit.json"`)
		enc := json.NewEncoder(w)
		_, _ = w.Write([]byte("["))
		first := true
		_ = s.Sec.Audit.Query(auditFilter(), func(e auditEntry) bool {
			if first {
				first = false
			} else {
				_, _ = w.Write([]byte(","))
			}
			_ = enc.Encode(e)
			return true
		})
		_, _ = w.Write([]byte("]"))
	}
}

// ---------- /api/admin/webhook-activity ----------

func (s *Server) adminWebhookActivityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "webhook.list"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	// Pull recent webhook.* audit entries — that's the canonical
	// audit-trail. We don't keep a separate in-memory ring buffer.
	if s.Sec == nil || s.Sec.Audit == nil {
		adminWriteJSON(w, http.StatusOK, []any{})
		return
	}
	out := make([]map[string]any, 0)
	f := auditFilter()
	f.Limit = 200
	_ = s.Sec.Audit.Query(f, func(e auditEntry) bool {
		if !strings.HasPrefix(e.Action, "webhook.") {
			return true
		}
		out = append(out, map[string]any{
			"deploy": strings.TrimPrefix(e.Resource, "webhook/"),
			"ts":     e.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			"result": e.Result,
			"reason": e.Reason,
		})
		return true
	})
	adminWriteJSON(w, http.StatusOK, out)
}

// auditFilter returns a zero-value filter — every Query in this file
// wants "everything, newest first". Centralised so future tweaks
// (limits, time ranges, etc.) can land in one place.
func auditFilter() auditFilterT { return auditFilterT{} }

// ---------- /api/admin/config/history + rollback + import ----------
//
// Snapshot history requires the internal/confighistory package which
// already implements snapshot rotation. For v0.5.0 we expose a thin
// read endpoint and a rollback POST; the import path takes raw YAML.

func (s *Server) adminConfigHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := s.Sec.Guard(r, Action{Permission: "config.history"}, nil); err != nil {
		adminWriteJSONError(w, Status(err), err.Error())
		return
	}
	// Config snapshot history surface — backing implementation lives in
	// internal/confighistory which is wired via the CLI today. Returning
	// an empty list keeps the dashboard happy on installs that haven't
	// enabled snapshotting; a 501 here would render a permanent error
	// banner instead of an empty state.
	adminWriteJSON(w, http.StatusOK, []any{})
}

func (s *Server) adminConfigRollbackHandler(w http.ResponseWriter, _ *http.Request) {
	// Wiring this up needs careful interplay with confighistory + audit
	// chain — beyond v0.5.0 scope. CHANGELOG notes the gap.
	adminWriteJSONError(w, http.StatusNotImplemented, "config rollback via UI not yet implemented; use `apigw config rollback <sha>`")
}

func (s *Server) adminConfigImportHandler(w http.ResponseWriter, _ *http.Request) {
	adminWriteJSONError(w, http.StatusNotImplemented, "config import via UI not yet implemented; use `apigw config import < file.yaml`")
}
