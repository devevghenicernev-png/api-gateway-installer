// Security wiring for the dashboard's admin API.
//
// Bundles the RBAC engine, OPA-policy engine, hash-chained audit log,
// multi-party approvals store, and tenant registry. Constructed once per
// process by NewSecurity() and held on Server.Sec. Every admin handler
// routes its writes through Sec.Guard() which:
//
//  1. Authenticates the request (bearer token → Identity, or anonymous).
//  2. Performs an RBAC check for the requested permission.
//  3. Runs OPA policies against the before/after payload.
//  4. (Optionally) parks the change as an approval request if the action
//     is in DangerousActions and the threshold > 0.
//  5. Calls onCommit() to perform the real mutation.
//  6. Writes an audit entry capturing actor / action / resource / result.
//
// When no Security config is provided in config.yaml the engine runs in
// legacy mode: requests are anonymous, every action is allowed, audit is
// still written if a state dir exists.
package dashboard

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/alerts"
	"github.com/devevghenicernev-png/apigw/internal/approvals"
	"github.com/devevghenicernev-png/apigw/internal/audit"
	"github.com/devevghenicernev-png/apigw/internal/changewindow"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/paths"
	"github.com/devevghenicernev-png/apigw/internal/policy"
	"github.com/devevghenicernev-png/apigw/internal/rbac"
	"github.com/devevghenicernev-png/apigw/internal/tenant"
)

// Security is the integrated guard-rails layer. All fields are nil-safe —
// nil Security means "no security configured" and Guard() short-circuits.
type Security struct {
	RBAC          *rbac.Engine
	Policy        *policy.Engine
	Audit         *audit.Logger
	Approvals     *approvals.Store
	Tenants       *tenant.Registry
	Alerts        *alerts.Dispatcher
	ChangeWindows []changewindow.Window // freeze periods that block mutating actions

	// teamsByUser maps a user identity to the team names they belong
	// to. Built from Security.Teams at NewSecurity / Reload time;
	// consulted on every Identify() to expand the user's Groups with
	// synthetic `team:<name>` entries.
	teamsByUser map[string][]string

	// Token table maps `Authorization: Bearer <token>` to an identity.
	// Tokens are kept in memory (read from config on Reload()).
	tokensMu sync.RWMutex
	tokens   map[string]rbac.Identity // sha256(token-bytes) → identity

	csrfKey    []byte // signing key for CSRF tokens issued to the dashboard
	maxBody    int64
	threshold  int
	stateDir   string
	enforce    bool
	auditReads bool // when true, even successful read-only operations land in audit.db
	logger     *slog.Logger
}

// NewSecurity constructs the integrated security layer from a Config block.
// Failures opening audit/approvals databases are logged but degrade the
// feature rather than block startup — the admin API still works without
// audit if the disk is full, etc.
func NewSecurity(cfg config.Security, tenants []config.Tenant, alertsCfg config.Alerts, logger *slog.Logger) (*Security, error) {
	if logger == nil {
		logger = slog.Default()
	}
	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = paths.StateDir()
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("security: state dir: %w", err)
	}
	s := &Security{
		stateDir:      stateDir,
		enforce:       cfg.RBACEnforce,
		maxBody:       cfg.MaxRequestBytes,
		threshold:     cfg.ApprovalsThreshold,
		auditReads:    cfg.AuditReads,
		logger:        logger,
		tokens:        map[string]rbac.Identity{},
		ChangeWindows: configChangeWindows(cfg.ChangeWindows),
		teamsByUser:   buildTeamIndex(cfg.Teams),
	}
	if s.maxBody <= 0 {
		s.maxBody = 1 << 20 // 1 MiB
	}

	// Audit first — the RBAC hook needs it.
	if l, err := audit.Open(stateDir); err == nil {
		s.Audit = l
	} else {
		logger.Warn("audit log unavailable", "err", err)
	}

	auditHook := func(actor, action, resource, result, reason string) {
		if s.Audit == nil {
			return
		}
		// RBAC.Check fires this hook before Guard's own recordAudit. On
		// success Guard writes the full picture (actor + before/after);
		// on denial Guard short-circuits and never calls recordAudit, so
		// we MUST write the denial here. Net effect:
		//   - success →    skipped here, recorded once by Guard
		//   - denied  →    recorded here (Guard never runs)
		// This is the single source of truth for audit entries — no
		// duplicates, no misses.
		if result == "ok" {
			return
		}
		_, _ = s.Audit.Log(audit.Entry{
			Actor:    actor,
			Action:   action,
			Resource: resource,
			Result:   result,
			Reason:   reason,
		})
	}

	// RBAC.
	s.RBAC = rbac.NewEngine(cfg.RBACEnforce, auditHook)
	extraRoles := make([]rbac.Role, 0, len(cfg.Roles))
	for _, r := range cfg.Roles {
		perms := make([]rbac.Permission, len(r.Permissions))
		for i, p := range r.Permissions {
			perms[i] = rbac.Permission(p)
		}
		extraRoles = append(extraRoles, rbac.Role{Name: r.Name, Description: r.Description, Permissions: perms})
	}
	assignments := make([]rbac.Assignment, 0, len(cfg.Assignments))
	for _, a := range cfg.Assignments {
		assignments = append(assignments, rbac.Assignment{User: a.User, Group: a.Group, Roles: a.Roles})
	}
	s.RBAC.LoadConfig(extraRoles, assignments)

	// Tokens.
	for _, t := range cfg.AdminTokens {
		if t.Token == "" || t.User == "" {
			continue
		}
		s.tokens[hashToken(t.Token)] = rbac.Identity{User: t.User, Groups: t.Groups}
	}

	// Policy.
	s.Policy = policy.NewEngine()
	if cfg.PolicyDir != "" {
		if n, err := s.Policy.LoadDir(cfg.PolicyDir); err != nil {
			logger.Warn("policy load failed", "dir", cfg.PolicyDir, "err", err)
		} else if n > 0 {
			logger.Info("policy loaded", "count", n)
		}
	}

	// Approvals.
	if store, err := approvals.Open(stateDir); err == nil {
		s.Approvals = store
	} else {
		logger.Warn("approvals store unavailable", "err", err)
	}

	// Tenants.
	if len(tenants) > 0 {
		s.Tenants = tenant.NewRegistry()
		ts := make([]tenant.Tenant, len(tenants))
		for i, t := range tenants {
			ts[i] = tenant.Tenant{
				ID: t.ID, Name: t.Name, Description: t.Description,
				PathPrefix: t.PathPrefix, Admins: t.Admins, Enabled: t.Enabled,
				Quotas: tenant.Quotas{MaxAPIs: t.MaxAPIs, MaxDeploys: t.MaxDeploys},
			}
		}
		if err := s.Tenants.LoadConfig(ts); err != nil {
			logger.Warn("tenant load failed", "err", err)
		}
	}

	// Alerts dispatcher.
	s.Alerts = alerts.New(slogAlertsAdapter{logger})
	if alertsCfg.Slack != "" {
		s.Alerts.Register(alerts.NewSlack(alertsCfg.Slack))
	}
	if alertsCfg.Teams != "" {
		s.Alerts.Register(alerts.NewTeams(alertsCfg.Teams))
	}
	if alertsCfg.PagerDuty != "" {
		s.Alerts.Register(alerts.NewPagerDuty(alertsCfg.PagerDuty))
	}
	if alertsCfg.Webhook.URL != "" {
		s.Alerts.Register(alerts.NewWebhook(alertsCfg.Webhook.URL, alertsCfg.Webhook.Headers))
	}
	if alertsCfg.Email.Host != "" && len(alertsCfg.Email.To) > 0 {
		s.Alerts.Register(&alerts.EmailNotifier{
			SMTPHost: alertsCfg.Email.Host,
			SMTPPort: alertsCfg.Email.Port,
			Username: alertsCfg.Email.Username,
			Password: alertsCfg.Email.Password,
			From:     alertsCfg.Email.From,
			To:       alertsCfg.Email.To,
		})
	}

	// CSRF key — config-provided or cached.
	if cfg.CSRFSecret != "" {
		s.csrfKey = []byte(cfg.CSRFSecret)
	} else {
		s.csrfKey = loadOrCreateCSRFKey(stateDir, logger)
	}

	return s, nil
}

// Close releases the bbolt handles. Safe on a nil receiver.
func (s *Security) Close() {
	if s == nil {
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Close()
	}
	if s.Approvals != nil {
		_ = s.Approvals.Close()
	}
}

// ReloadTokens re-reads the admin-token table from a fresh config and
// swaps it atomically under tokensMu. This is the hot-reload path used
// by `apigw auth admin token revoke` so a revoked credential stops
// working immediately, without restarting the dashboard.
//
// Roles, role assignments, threshold, OPA policies are all also
// refreshed since they're cheap and share the same config block —
// keeping them in sync prevents a state where "token works, but RBAC
// disagrees about its roles".
func (s *Security) ReloadTokens(cfg config.Security) {
	if s == nil {
		return
	}
	s.tokensMu.Lock()
	s.tokens = map[string]rbac.Identity{}
	for _, t := range cfg.AdminTokens {
		if t.Token == "" || t.User == "" {
			continue
		}
		s.tokens[hashToken(t.Token)] = rbac.Identity{User: t.User, Groups: t.Groups}
	}
	s.tokensMu.Unlock()

	// Refresh RBAC role assignments too, so revoking a token AND
	// changing the user's roles in the same edit takes effect
	// immediately.
	if s.RBAC != nil {
		extra := make([]rbac.Role, 0, len(cfg.Roles))
		for _, r := range cfg.Roles {
			perms := make([]rbac.Permission, len(r.Permissions))
			for i, p := range r.Permissions {
				perms[i] = rbac.Permission(p)
			}
			extra = append(extra, rbac.Role{Name: r.Name, Description: r.Description, Permissions: perms})
		}
		asg := make([]rbac.Assignment, 0, len(cfg.Assignments))
		for _, a := range cfg.Assignments {
			asg = append(asg, rbac.Assignment{User: a.User, Group: a.Group, Roles: a.Roles})
		}
		s.RBAC.LoadConfig(extra, asg)
	}
	s.threshold = cfg.ApprovalsThreshold
	s.auditReads = cfg.AuditReads
}

// Identify extracts the caller's RBAC identity from request headers.
// Anonymous when no token is presented OR token is unknown.
func (s *Security) Identify(r *http.Request) rbac.Identity {
	if s == nil {
		return rbac.Identity{User: "anonymous"}
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		// nginx auth_request flow can also forward an X-Apigw-Subject header
		// set by the JWT verifier. Accept it as the identity.
		if sub := r.Header.Get("X-Apigw-Subject"); sub != "" {
			return s.expandTeams(rbac.Identity{User: sub})
		}
		return rbac.Identity{User: "anonymous"}
	}
	tok := strings.TrimPrefix(h, "Bearer ")
	tok = strings.TrimPrefix(tok, "bearer ")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return rbac.Identity{User: "anonymous"}
	}
	s.tokensMu.RLock()
	ident, ok := s.tokens[hashToken(tok)]
	s.tokensMu.RUnlock()
	if !ok {
		return rbac.Identity{User: "anonymous"}
	}
	return s.expandTeams(ident)
}

// expandTeams appends synthetic `team:<name>` entries to the
// identity's Groups for every team the user is in. No-op when no
// teams are configured. Returns the (possibly extended) identity by
// value — we never mutate the original (it lives in the token map).
func (s *Security) expandTeams(in rbac.Identity) rbac.Identity {
	if s == nil || len(s.teamsByUser) == 0 {
		return in
	}
	teams := s.teamsByUser[in.User]
	if len(teams) == 0 {
		return in
	}
	out := rbac.Identity{User: in.User, Groups: append([]string(nil), in.Groups...)}
	for _, t := range teams {
		out.Groups = append(out.Groups, "team:"+t)
	}
	return out
}

// Action describes a guarded mutation. Subject = "deploy/foo" for resource
// tracking; Before/After are pre/post snapshots fed to OPA.
type Action struct {
	Permission rbac.Permission
	Resource   string
	Before     map[string]any
	After      map[string]any
	Dangerous  bool // if true & ApprovalsThreshold>0, park instead of commit
}

// Guard runs auth → RBAC → policy → approvals → commit → audit. The
// onCommit closure does the actual mutation; it's only invoked once every
// gate passes. Returns the audit entry ID for inclusion in the response.
//
// Errors propagate to the HTTP handler which decides the status code.
// Use Status() to map common errors to HTTP codes.
func (s *Security) Guard(r *http.Request, a Action, onCommit func() error) (uint64, error) {
	ident := s.Identify(r)

	// 0. Change windows — block mutating actions during freeze
	// periods. Read-only actions (.list/.show/.get/.read/.status) pass.
	if s != nil && len(s.ChangeWindows) > 0 && changewindow.IsMutating(string(a.Permission)) {
		if w := changewindow.Active(s.ChangeWindows, time.Now()); w != nil {
			reason := w.Reason
			if reason == "" {
				if w.Name != "" {
					reason = "freeze window: " + w.Name
				} else {
					reason = "freeze window active"
				}
			}
			_, _ = s.recordAudit(ident.User, a, "denied", reason)
			return 0, fmt.Errorf("%w: %s", ErrChangeFrozen, reason)
		}
	}

	// 1. RBAC.
	if s != nil && s.RBAC != nil {
		if err := s.RBAC.Check(ident, a.Permission, a.Resource); err != nil {
			return 0, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
	}

	// 2. Policy (OPA Rego).
	if s != nil && s.Policy != nil {
		input := map[string]any{
			"actor":    ident.User,
			"groups":   ident.Groups,
			"action":   string(a.Permission),
			"resource": a.Resource,
			"before":   a.Before,
			"after":    a.After,
		}
		decision, err := s.Policy.Evaluate(r.Context(), input)
		if err != nil {
			_, _ = s.recordAudit(ident.User, a, "failed", "policy: "+err.Error())
			return 0, fmt.Errorf("policy: %w", err)
		}
		if decision.Denied {
			reason := strings.Join(decision.Reasons, "; ")
			_, _ = s.recordAudit(ident.User, a, "denied", reason)
			return 0, fmt.Errorf("%w: %s", ErrPolicyDenied, reason)
		}
	}

	// 3. Approvals (gate dangerous actions behind N-of-M sign-off).
	if s != nil && a.Dangerous && s.threshold > 0 && s.Approvals != nil {
		payload := map[string]any{"before": a.Before, "after": a.After}
		cr, err := s.Approvals.Submit(ident.User, string(a.Permission), a.Resource, payload, s.threshold, 24*time.Hour)
		if err != nil {
			if errors.Is(err, approvals.ErrDuplicate) {
				// Same operator (or anyone) already opened this change request.
				// Surface the EXISTING id so reviewers don't have to chase
				// a phantom second request, and don't double-audit.
				return 0, fmt.Errorf("%w: change %s is already pending — share that id",
					ErrPendingApproval, cr.ID)
			}
			return 0, fmt.Errorf("approvals submit: %w", err)
		}
		_, _ = s.recordAudit(ident.User, a, "parked", "approval id="+cr.ID)
		return 0, fmt.Errorf("%w: change %s parked, need %d approvals", ErrPendingApproval, cr.ID, s.threshold)
	}

	// 4. Commit.
	if onCommit != nil {
		if err := onCommit(); err != nil {
			_, _ = s.recordAudit(ident.User, a, "failed", err.Error())
			return 0, err
		}
	}

	// 5. Audit success.
	id, aerr := s.recordAudit(ident.User, a, "ok", "")
	if aerr != nil {
		return 0, aerr
	}
	return id, nil
}

// ReadBody reads up to MaxRequestBytes from r.Body into target via json.Unmarshal.
// Wrap admin handlers with this instead of json.NewDecoder(r.Body).Decode —
// defends against memory-DoS via an unbounded request.
func (s *Security) ReadBody(r *http.Request, target any) error {
	max := int64(1 << 20)
	if s != nil && s.maxBody > 0 {
		max = s.maxBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > max {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrBadRequest, max)
	}
	return json.Unmarshal(body, target)
}

// RecordDenial writes a single denial entry. Used by admin handlers when
// CSRF / auth fails BEFORE Guard() is reached — without this, those
// rejections would be invisible to the audit log because the RBAC hook
// never fires.
func (s *Security) RecordDenial(r *http.Request, permission rbac.Permission, resource, reason string) {
	if s == nil || s.Audit == nil {
		return
	}
	ident := s.Identify(r)
	_, _ = s.Audit.Log(audit.Entry{
		Actor:    ident.User,
		Action:   string(permission),
		Resource: resource,
		Result:   "denied",
		Reason:   reason,
	})
}

// IssueCSRFToken returns an HMAC-signed token tied to the identity. The
// dashboard JS reads it from /api/admin/csrf and echoes it back in
// X-CSRF-Token on every write. Tokens expire after 12h.
func (s *Security) IssueCSRFToken(user string) string {
	if s == nil {
		return ""
	}
	ts := time.Now().Unix()
	payload := fmt.Sprintf("%s|%d", user, ts)
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.URLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// VerifyCSRFToken returns nil if the token is valid for the given user and
// within the 12h validity window. Constant-time signature compare.
//
// All failure modes map to ErrForbidden — a malformed token is just as
// unauthorised as a missing/wrong one, and returning a distinct 400 on
// "bad base64" would let an attacker tell apart "we accept this user but
// reject your token" from "your token is corrupt", which is a small but
// real probing oracle.
func (s *Security) VerifyCSRFToken(user, token string) error {
	if s == nil {
		return nil // CSRF off when security off
	}
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return ErrForbidden
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return ErrForbidden
	}
	if parts[0] != user {
		return ErrForbidden
	}
	var ts int64
	if _, err := fmt.Sscanf(parts[1], "%d", &ts); err != nil {
		return ErrForbidden
	}
	if time.Since(time.Unix(ts, 0)) > 12*time.Hour {
		return ErrForbidden
	}
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(parts[0] + "|" + parts[1]))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return ErrForbidden
	}
	return nil
}

// FireAlert sends an event to every registered notifier. No-op when no
// alerts dispatcher is configured.
func (s *Security) FireAlert(category, severity, title, detail, resource string) {
	if s == nil || s.Alerts == nil {
		return
	}
	go s.Alerts.Fire(context.Background(), alerts.Event{
		Category: category,
		Severity: severity,
		Title:    title,
		Detail:   detail,
		Resource: resource,
		Time:     time.Now().UTC(),
	})
}

// AuditCount returns the total number of audit entries (cheap lookup).
func (s *Security) AuditCount() uint64 {
	if s == nil || s.Audit == nil {
		return 0
	}
	n, _ := s.Audit.Count()
	return n
}

// ---------- errors ----------

var (
	ErrForbidden        = errors.New("forbidden")
	ErrPolicyDenied     = errors.New("policy denied")
	ErrPendingApproval  = errors.New("pending approval")
	ErrBadRequest       = errors.New("bad request")
	ErrAuditUnavailable = errors.New("audit log unavailable")
	ErrChangeFrozen     = errors.New("change frozen")
)

// Status maps an error returned by Guard()/ReadBody() to an HTTP status.
func Status(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrPolicyDenied):
		return http.StatusForbidden
	case errors.Is(err, ErrPendingApproval):
		return http.StatusAccepted
	case errors.Is(err, ErrChangeFrozen):
		return http.StatusLocked
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrAuditUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// ---------- internals ----------

// recordAudit persists an Entry, EXCEPT when the operation is a successful
// read-only lookup (.list / .show / .query / .status). Two reasons:
//
//  1. The embedded dashboard polls /api/admin/{apis,audit,approvals,...}
//     every 10s; without this filter every signed-in operator generates
//     ~6 audit entries per minute of pure noise.
//  2. SOX / PCI / SOC2 require attribution of MUTATIONS, not lookups.
//     Anomalous read patterns (e.g. enumeration) still surface because
//     DENIED reads are always recorded — see the result check below.
//
// To capture every read for highest-paranoia mode (defence-in-depth on a
// compromised dashboard), set security.audit_reads: true in config.
// On MUTATION audit-write failure we propagate ErrAuditUnavailable so
// Guard refuses the request — silently mutating state without a
// hash-chain entry breaks SOX/PCI compliance and our own forensics.
// Read-only audit-write failures stay warning-only since blocking the
// API entirely on log-storage problems is worse than degraded auditing.
func (s *Security) recordAudit(actor string, a Action, result, reason string) (uint64, error) {
	if s == nil || s.Audit == nil {
		return 0, nil
	}
	if result == "ok" && !s.auditReads && isReadOnlyPermission(a.Permission) {
		return 0, nil
	}
	e, err := s.Audit.Log(audit.Entry{
		Actor:    actor,
		Action:   string(a.Permission),
		Resource: a.Resource,
		Result:   result,
		Reason:   reason,
		Before:   a.Before,
		After:    a.After,
	})
	if err != nil {
		s.logger.Warn("audit log failed", "result", result, "err", err)
		if result == "ok" && !isReadOnlyPermission(a.Permission) {
			return 0, fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		return 0, nil
	}
	return e.ID, nil
}

// isReadOnlyPermission returns true when the permission verb is a lookup
// (list / show / query / status). Anything else is considered a state
// change and always lands in the audit log.
func isReadOnlyPermission(p rbac.Permission) bool {
	s := string(p)
	for _, suffix := range []string{".list", ".show", ".query", ".status"} {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// loadOrCreateCSRFKey returns the persisted 32-byte CSRF key, or
// generates one and atomically persists it via O_CREATE|O_EXCL. The
// exclusive create rejects a race where two dashboard processes (or
// dashboards started simultaneously by a misconfigured systemd) both
// try to mint a key — without it, last-write-wins meant tokens issued
// by one process silently failed verification on the other. On a race
// loser we re-read the file written by the winner.
func loadOrCreateCSRFKey(dir string, logger *slog.Logger) []byte {
	path := filepath.Join(dir, "csrf.key")
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		logger.Warn("csrf key: rand read failed; using static", "err", err)
		copy(key, []byte("apigw-fallback-csrf-key-DO-NOT-USE!"))
	}
	// Exclusive create — if another process already created the file
	// between our ReadFile above and this OpenFile, we lose the race
	// gracefully and adopt their key.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if existing, rerr := os.ReadFile(path); rerr == nil && len(existing) == 32 {
			return existing
		}
		logger.Warn("csrf key persist failed", "err", err)
		return key
	}
	if _, werr := f.Write(key); werr != nil {
		logger.Warn("csrf key write failed", "err", werr)
	}
	_ = f.Close()
	_ = os.Chmod(path, 0o600)
	return key
}

// slogAlertsAdapter satisfies alerts.Logger using a *slog.Logger.
type slogAlertsAdapter struct{ l *slog.Logger }

func (a slogAlertsAdapter) Info(msg string, fields ...any)  { a.l.Info(msg, fields...) }
func (a slogAlertsAdapter) Warn(msg string, fields ...any)  { a.l.Warn(msg, fields...) }
func (a slogAlertsAdapter) Error(msg string, fields ...any) { a.l.Error(msg, fields...) }

// configChangeWindows converts the config-side ChangeWindow slice into
// the changewindow package's Window slice (avoids dragging the config
// package into changewindow itself).
func configChangeWindows(in []config.ChangeWindow) []changewindow.Window {
	if len(in) == 0 {
		return nil
	}
	out := make([]changewindow.Window, 0, len(in))
	for _, w := range in {
		out = append(out, changewindow.Window{
			Name:      w.Name,
			Days:      w.Days,
			StartHour: w.StartHour,
			EndHour:   w.EndHour,
			Reason:    w.Reason,
		})
	}
	return out
}

// buildTeamIndex inverts the user-by-team config into a team-by-user
// map for cheap Identify-time lookup.
func buildTeamIndex(teams []config.Team) map[string][]string {
	if len(teams) == 0 {
		return nil
	}
	out := make(map[string][]string)
	for _, t := range teams {
		for _, m := range t.Members {
			out[m] = append(out[m], t.Name)
		}
	}
	return out
}
