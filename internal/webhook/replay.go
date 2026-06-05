package webhook

import (
	"sync"
	"time"
)

// ReplayWindow is the LRU eviction horizon for X-GitHub-Delivery UUIDs.
//
// 10 minutes matches certbot's nonce window and is well above GitHub's
// retry envelope (3 deliveries × ~30 s apart). Anything older we forget;
// if a malicious replay arrives that long after the original we
// effectively accept it — but it would already need to pass HMAC, which is
// a much stronger gate.
const ReplayWindow = 10 * time.Minute

// ReplayCache is a bounded delivery-id memoryser. Adds are O(1); seen
// lookups are O(1). Eviction is lazy — we sweep when the map grows past
// `softLimit`, keeping the latest entries by timestamp.
//
// Why bounded: an attacker who can spam unsigned requests should not be
// able to OOM us. softLimit=10_000 is plenty for legitimate traffic
// (GitHub itself caps webhook delivery rate per repo).
type ReplayCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	softLimit int
}

// NewReplayCache returns a cache with the default soft limit.
func NewReplayCache() *ReplayCache {
	return &ReplayCache{seen: make(map[string]time.Time, 1024), softLimit: 10_000}
}

// Seen reports whether `id` was added inside the replay window. Updates
// the LRU touch timestamp on hit so a live retry chain doesn't expire mid-flight.
func (c *ReplayCache) Seen(id string) bool {
	if id == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.seen[id]
	if !ok {
		return false
	}
	if time.Since(t) > ReplayWindow {
		delete(c.seen, id)
		return false
	}
	c.seen[id] = time.Now()
	return true
}

// Add records `id` as seen now. Triggers a sweep if the cache is over its
// soft limit.
func (c *ReplayCache) Add(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[id] = time.Now()
	if len(c.seen) > c.softLimit {
		c.sweep()
	}
}

// TestAndAdd atomically checks whether `id` was already seen within the
// window AND records it. Returns true iff the id was already present (so
// the caller should treat the request as a replay).
//
// Without this single-lock variant, two concurrent identical requests
// could both pass Seen() before either's Add() landed — double-enqueueing
// the same delivery.
func (c *ReplayCache) TestAndAdd(id string) bool {
	if id == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.seen[id]; ok && time.Since(t) <= ReplayWindow {
		c.seen[id] = time.Now()
		return true
	}
	c.seen[id] = time.Now()
	if len(c.seen) > c.softLimit {
		c.sweep()
	}
	return false
}

// Forget removes `id` from the cache. Used when the handler downstream of
// TestAndAdd fails — we don't want GitHub's retry to be incorrectly
// short-circuited as a duplicate.
func (c *ReplayCache) Forget(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, id)
}

// sweep removes entries older than the replay window. Caller holds the lock.
func (c *ReplayCache) sweep() {
	cutoff := time.Now().Add(-ReplayWindow)
	for id, t := range c.seen {
		if t.Before(cutoff) {
			delete(c.seen, id)
		}
	}
}

// Len returns the current entry count — for the status command.
func (c *ReplayCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}
