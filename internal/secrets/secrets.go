// Package secrets resolves opaque secret references to plaintext at use
// time. Config values that look like `vault://kv/data/apigw/jwt`,
// `aws-sm://billing/jwt-secret`, or `azure-kv://my-vault/jwt-secret` are
// dispatched to the matching provider; everything else passes through
// as-is so file-based secrets and plain strings still work.
//
// Why lazy resolution? Enterprise rotates secrets without redeploying.
// If we resolved at config-load, every rotation would need an apigw
// restart. Per-use resolution means a rotated secret is picked up on
// next request — no downtime.
//
// Provider interface is tiny on purpose. Each impl wraps the vendor SDK.
// Built-in: file (default, no external service), env, vault, aws-sm,
// azure-kv.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider resolves a secret URI to plaintext bytes. Implementations should
// cache by URI for the configured TTL and respect ctx for cancellation.
type Provider interface {
	Scheme() string
	Resolve(ctx context.Context, uri string) (string, error)
}

// Resolver dispatches URIs to the right Provider by scheme prefix.
// Construct via NewResolver and register providers before use.
type Resolver struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewResolver() *Resolver {
	r := &Resolver{providers: map[string]Provider{}}
	// File provider is always present so `file://...` and bare paths work
	// out of the box without configuration.
	r.Register(&fileProvider{})
	r.Register(&envProvider{})
	return r
}

// Register adds a provider; replaces any previous one with the same scheme.
func (r *Resolver) Register(p Provider) {
	r.mu.Lock()
	r.providers[p.Scheme()] = p
	r.mu.Unlock()
}

// Resolve dispatches `ref` to the matching provider. References without a
// scheme (`my-secret`) pass through unchanged — treat them as literal
// values. References with a scheme that has no registered provider return
// ErrUnknownScheme.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	scheme := schemeOf(ref)
	if scheme == "" {
		// Plain string — literal value.
		return ref, nil
	}
	r.mu.RLock()
	p, ok := r.providers[scheme]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownScheme, scheme)
	}
	return p.Resolve(ctx, ref)
}

// ErrUnknownScheme is returned when a URI references a provider that
// wasn't registered (e.g. `vault://...` without the vault provider).
var ErrUnknownScheme = errors.New("secrets: unknown scheme")

// schemeOf extracts the URL scheme. We don't use net/url because vault
// URIs aren't strictly RFC-3986 (they have `data/` not `data?`). Treat
// the prefix before `://` as the scheme.
func schemeOf(ref string) string {
	i := strings.Index(ref, "://")
	if i < 0 {
		return ""
	}
	return ref[:i]
}

// cachedSecret wraps a value with an expiry. Providers use this to honor
// rotation: stale entries trigger a refetch.
type cachedSecret struct {
	value     string
	fetchedAt time.Time
}

// genericCache is a tiny TTL cache shared across providers.
type genericCache struct {
	mu  sync.RWMutex
	ttl time.Duration
	m   map[string]cachedSecret
}

func newCache(ttl time.Duration) *genericCache {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	return &genericCache{ttl: ttl, m: map[string]cachedSecret{}}
}

func (c *genericCache) get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cs, ok := c.m[key]
	if !ok || time.Since(cs.fetchedAt) > c.ttl {
		return "", false
	}
	return cs.value, true
}

func (c *genericCache) set(key, value string) {
	c.mu.Lock()
	c.m[key] = cachedSecret{value: value, fetchedAt: time.Now()}
	c.mu.Unlock()
}

// Silence unused-import on builds where url is dropped.
var _ = url.Parse
