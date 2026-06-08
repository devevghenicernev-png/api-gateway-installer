// Package rbac is apigw's role-based access control.
//
// Model: User → Role → Permissions. A Permission is a dot-namespaced verb
// matching the CLI: "api.add", "deploy.apply", "secret.rotate", etc.
// Wildcards: "deploy.*" grants all deploy verbs; "*" grants everything.
//
// Roles ship as YAML in the config. Built-in roles are pre-loaded:
//
//	viewer    — read-only: *.list, *.status, *.show, doctor, audit.query
//	operator  — viewer + deploy.apply, deploy.run, webhook.*, api.reload
//	admin     — operator + api.*, deploy.*, tls.*, auth.*, secret.*
//	owner     — everything: *
//
// Users are mapped to roles via:
//  1. Config-level static assignments (single-host installs)
//  2. LDAP group → role mapping (E6)
//  3. SAML attribute → role mapping (E7)
//
// Every Check() that denies an action is logged to the audit package so
// SIEM sees the attempt.
package rbac

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Permission is a dot-namespaced verb, e.g. "deploy.apply". Empty parts
// are forbidden ("deploy." is invalid).
type Permission string

// Role is a named bundle of permissions.
type Role struct {
	Name        string       `koanf:"name" yaml:"name"`
	Description string       `koanf:"description" yaml:"description,omitempty"`
	Permissions []Permission `koanf:"permissions" yaml:"permissions"`
}

// Assignment maps a user (or group, when LDAP/SAML is wired) to one or
// more roles. Multiple roles are additive: union of all permissions.
type Assignment struct {
	User  string   `koanf:"user" yaml:"user"`             // exact match
	Group string   `koanf:"group" yaml:"group,omitempty"` // LDAP/SAML group match
	Roles []string `koanf:"roles" yaml:"roles"`
}

// Engine is the runtime: cached role map + assignment table.
type Engine struct {
	mu        sync.RWMutex
	roles     map[string]Role
	users     map[string][]string // user → role names
	groups    map[string][]string // group → role names
	enforce   bool                // false = legacy mode (log only, never deny)
	auditFunc func(actor, action, resource, result, reason string)
}

// NewEngine constructs a configured Engine.
//
// `enforce=false` is the migration mode: every Check() returns ok but the
// denial would-be is logged. Useful for the first weeks after rolling out
// RBAC so operators can see which users hit which limits before the
// hard-enforce flip.
func NewEngine(enforce bool, audit func(actor, action, resource, result, reason string)) *Engine {
	e := &Engine{
		roles:     map[string]Role{},
		users:     map[string][]string{},
		groups:    map[string][]string{},
		enforce:   enforce,
		auditFunc: audit,
	}
	for _, r := range BuiltinRoles() {
		e.roles[r.Name] = r
	}
	return e
}

// LoadConfig replaces the dynamic state from a deserialized config block.
// Existing built-in roles stay; user-defined roles with the same name
// override them.
func (e *Engine) LoadConfig(extraRoles []Role, assignments []Assignment) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Re-seed with built-ins so removed user-defined roles disappear.
	e.roles = map[string]Role{}
	for _, r := range BuiltinRoles() {
		e.roles[r.Name] = r
	}
	for _, r := range extraRoles {
		e.roles[r.Name] = r
	}

	// Dedup as we go: a config file with two `Assignment{User: "alice"}`
	// blocks (intentional or copy-paste) would otherwise double-count
	// roles. Append-then-dedup is cheaper than repeated linear scans for
	// our small N (≤ a few hundred users per install).
	e.users = map[string][]string{}
	e.groups = map[string][]string{}
	for _, a := range assignments {
		if a.User != "" {
			e.users[a.User] = append(e.users[a.User], a.Roles...)
		}
		if a.Group != "" {
			e.groups[a.Group] = append(e.groups[a.Group], a.Roles...)
		}
	}
	for k, v := range e.users {
		e.users[k] = dedupStrings(v)
	}
	for k, v := range e.groups {
		e.groups[k] = dedupStrings(v)
	}
}

// dedupStrings preserves order and drops duplicates.
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// Identity is what Check() receives. User is the primary key; Groups are
// supplemental (from LDAP/SAML). Empty Identity (zero value) maps to the
// "anonymous" role-less user — denied for anything not in built-in
// "viewer".
type Identity struct {
	User   string
	Groups []string
}

// Check returns nil if the identity has the named permission. Returns
// ErrDenied when not allowed AND enforce=true; never returns ErrDenied
// when enforce=false (logged but allowed).
//
// `resource` is the affected entity (e.g. "billing", "deploy/foo");
// passed for audit context but not part of the permission match.
func (e *Engine) Check(ident Identity, perm Permission, resource string) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.userHas(ident, perm) {
		e.audit(ident.User, string(perm), resource, "ok", "")
		return nil
	}
	reason := fmt.Sprintf("user %q lacks %q", ident.User, perm)
	e.audit(ident.User, string(perm), resource, "denied", reason)
	if !e.enforce {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDenied, reason)
}

func (e *Engine) userHas(ident Identity, perm Permission) bool {
	// Collect all role names from user and group memberships.
	roleNames := map[string]struct{}{}
	for _, r := range e.users[ident.User] {
		roleNames[r] = struct{}{}
	}
	for _, g := range ident.Groups {
		for _, r := range e.groups[g] {
			roleNames[r] = struct{}{}
		}
	}
	for name := range roleNames {
		role, ok := e.roles[name]
		if !ok {
			continue
		}
		for _, p := range role.Permissions {
			if matches(p, perm) {
				return true
			}
		}
	}
	return false
}

// matches is the wildcard rule: "deploy.*" matches "deploy.apply", "*"
// matches anything. Exact strings match too.
func matches(grant, requested Permission) bool {
	g, r := string(grant), string(requested)
	if g == "*" || g == r {
		return true
	}
	if strings.HasSuffix(g, ".*") {
		prefix := g[:len(g)-1] // includes trailing "."
		return strings.HasPrefix(r, prefix)
	}
	return false
}

// Audit invokes the audit hook only when configured.
func (e *Engine) audit(actor, action, resource, result, reason string) {
	if e.auditFunc == nil {
		return
	}
	e.auditFunc(actor, action, resource, result, reason)
}

// ErrDenied is returned by Check() when the identity lacks the requested
// permission AND enforce=true.
var ErrDenied = errors.New("rbac: denied")

// BuiltinRoles returns the four reference roles. They're not stored in
// config — operators get them out of the box and can override by defining
// a role with the same name in the YAML.
func BuiltinRoles() []Role {
	return []Role{
		{
			Name:        "viewer",
			Description: "read-only access — listings, status, doctor",
			Permissions: []Permission{
				"api.list", "api.show",
				"deploy.list", "deploy.status", "deploy.logs",
				"webhook.list", "webhook.status",
				"tls.list", "tls.show",
				"auth.list", "auth.show",
				"stream.list", "stream.show",
				"consumer.list", "consumer.show",
				"gitops.show",
				"audit.query",
				"doctor",
				"status",
				"version",
			},
		},
		{
			Name:        "operator",
			Description: "viewer + deploy + webhook ops; no config changes",
			Permissions: []Permission{
				"viewer-base", // expanded via inheritance? we just list these inline
				"api.list", "api.show", "api.reload",
				"deploy.list", "deploy.status", "deploy.logs", "deploy.apply", "deploy.run", "deploy.restart",
				"webhook.*",
				"tls.list", "tls.show", "tls.renew",
				"auth.list", "auth.show",
				"audit.query",
				"doctor", "status", "version",
				"backup",
			},
		},
		{
			Name:        "admin",
			Description: "operator + all manage commands except RBAC + secrets",
			Permissions: []Permission{
				"api.*",
				"deploy.*",
				"webhook.*",
				"tls.*",
				"auth.*",
				"stream.*",
				"consumer.*",
				"gitops.*",
				"backup", "restore",
				"audit.*",
				"doctor", "status", "version",
				"migrate",
			},
		},
		{
			Name:        "owner",
			Description: "everything — RBAC management, secrets, cluster ops",
			Permissions: []Permission{"*"},
		},
	}
}
