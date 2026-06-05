// Package tenant implements multi-tenancy primitives for apigw.
//
// Model: a Tenant is a logical workspace with its own APIs, deploys,
// rate-limit quotas, RBAC assignments, and audit slice. One apigw process
// can serve many tenants — nginx generates per-tenant location prefixes
// like /t/acme/api/billing.
//
// Why tenants instead of separate instances? Hosting platforms and
// internal platforms-as-a-service need many low-traffic API groups behind
// one cert + one IP. Spinning a binary per tenant doesn't scale past
// ~50 tenants per host.
//
// Isolation guarantees:
//   - Per-tenant rate-limit zones (limit_req_zone has its own buckets)
//   - Per-tenant RBAC (admin of tenant A can't touch tenant B's APIs)
//   - Per-tenant audit (apigw audit query --tenant acme)
//   - Per-tenant TLS (separate certs per tenant if configured)
//
// What's NOT isolated (caveat):
//   - nginx worker processes — a runaway request can starve siblings
//   - upstream connection pools — if you need true isolation, run separate
//     apigw instances per high-value tenant (the "noisy neighbor" pattern)
package tenant

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
)

// Tenant is the workspace record.
type Tenant struct {
	ID          string   `koanf:"id" yaml:"id"`     // url-safe slug
	Name        string   `koanf:"name" yaml:"name"` // display name
	Description string   `koanf:"description" yaml:"description,omitempty"`
	PathPrefix  string   `koanf:"path_prefix" yaml:"path_prefix"` // e.g. /t/acme — every API path nests here
	Quotas      Quotas   `koanf:"quotas" yaml:"quotas,omitempty"`
	Admins      []string `koanf:"admins" yaml:"admins,omitempty"` // user IDs with admin role inside this tenant
	Enabled     bool     `koanf:"enabled" yaml:"enabled"`
}

// Quotas caps tenant resource usage. Zero = no limit.
type Quotas struct {
	MaxAPIs           int `koanf:"max_apis" yaml:"max_apis,omitempty"`
	MaxDeploys        int `koanf:"max_deploys" yaml:"max_deploys,omitempty"`
	MaxRequestsPerSec int `koanf:"max_rps" yaml:"max_rps,omitempty"` // aggregate across all tenant APIs
	MaxBodyBytes      int `koanf:"max_body_bytes" yaml:"max_body_bytes,omitempty"`
}

// Registry holds the current tenant set. One Registry per apigw process.
type Registry struct {
	mu      sync.RWMutex
	tenants map[string]*Tenant // by ID
}

func NewRegistry() *Registry {
	return &Registry{tenants: map[string]*Tenant{}}
}

// LoadConfig replaces the registry with a fresh slice.
func (r *Registry) LoadConfig(ts []Tenant) error {
	for _, t := range ts {
		if err := ValidateID(t.ID); err != nil {
			return fmt.Errorf("tenant %s: %w", t.ID, err)
		}
	}
	r.mu.Lock()
	r.tenants = map[string]*Tenant{}
	for i := range ts {
		t := ts[i]
		r.tenants[t.ID] = &t
	}
	r.mu.Unlock()
	return nil
}

// Add inserts a new tenant. Returns ErrAlreadyExists if the ID is taken.
func (r *Registry) Add(t Tenant) error {
	if err := ValidateID(t.ID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tenants[t.ID]; exists {
		return ErrAlreadyExists
	}
	if t.PathPrefix == "" {
		t.PathPrefix = "/t/" + t.ID
	}
	r.tenants[t.ID] = &t
	return nil
}

// Get returns the tenant by ID, or nil if missing.
func (r *Registry) Get(id string) *Tenant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tenants[id]
	if !ok {
		return nil
	}
	cp := *t
	return &cp
}

// List returns a snapshot of every tenant.
func (r *Registry) List() []Tenant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tenant, 0, len(r.tenants))
	for _, t := range r.tenants {
		out = append(out, *t)
	}
	return out
}

// Remove deletes the tenant. APIs/deploys/etc that referenced it become
// orphans — caller is responsible for cleanup OR refusing if non-empty.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[id]; !ok {
		return ErrNotFound
	}
	delete(r.tenants, id)
	return nil
}

// IsAdmin reports whether `user` has admin powers inside `tenantID`.
// Combined with RBAC: a user with system-level "admin" role bypasses
// tenant admin checks.
func (r *Registry) IsAdmin(tenantID, user string) bool {
	t := r.Get(tenantID)
	if t == nil {
		return false
	}
	for _, a := range t.Admins {
		if a == user {
			return true
		}
	}
	return false
}

// ValidateID enforces the slug shape: lowercase alphanumeric, hyphens
// allowed, 2-32 chars, no leading/trailing hyphen.
func ValidateID(id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("invalid tenant id %q (lowercase, 2-32 chars, alnum + hyphen)", id)
	}
	return nil
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// ErrNotFound / ErrAlreadyExists are the standard registry-state errors.
var (
	ErrNotFound      = errors.New("tenant: not found")
	ErrAlreadyExists = errors.New("tenant: already exists")
)
