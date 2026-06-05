package webhook

import (
	"testing"
	"time"
)

func TestReplayCache_HitMiss(t *testing.T) {
	c := NewReplayCache()
	if c.Seen("a") {
		t.Fatal("fresh cache reported 'a' as seen")
	}
	c.Add("a")
	if !c.Seen("a") {
		t.Fatal("Add then Seen should report hit")
	}
}

// TestReplayCache_EmptyID — defensive: callers may pass an empty
// X-GitHub-Delivery on broken clients; the cache must treat that as
// never-seen rather than collapsing them all together.
func TestReplayCache_EmptyID(t *testing.T) {
	c := NewReplayCache()
	c.Add("")
	if c.Seen("") {
		t.Fatal("empty id must never report as seen")
	}
	if c.Len() != 0 {
		t.Fatalf("empty Add must not grow the cache; got Len=%d", c.Len())
	}
}

// TestReplayCache_ExpiresOutsideWindow uses a manually-aged entry to
// confirm the window honours ReplayWindow. We poke c.seen directly
// because waiting 10 real minutes would make the test useless.
func TestReplayCache_ExpiresOutsideWindow(t *testing.T) {
	c := NewReplayCache()
	c.seen["old"] = time.Now().Add(-2 * ReplayWindow)
	if c.Seen("old") {
		t.Fatal("entry beyond window must not register as seen")
	}
	if _, ok := c.seen["old"]; ok {
		t.Fatal("Seen must evict expired entries it observes")
	}
}

func TestReplayCache_TouchOnHit(t *testing.T) {
	c := NewReplayCache()
	c.Add("k")
	c.seen["k"] = time.Now().Add(-1 * time.Minute) // age it within the window
	before := c.seen["k"]
	if !c.Seen("k") {
		t.Fatal("entry still within window must register as seen")
	}
	after := c.seen["k"]
	if !after.After(before) {
		t.Fatalf("Seen must touch the timestamp; before=%v after=%v", before, after)
	}
}

// TestReplayCache_Sweep ensures the cache stays bounded under spam.
// We force the soft limit down to a tiny number so a few Adds trigger the
// sweep code path without an unreasonable test runtime.
func TestReplayCache_Sweep(t *testing.T) {
	c := NewReplayCache()
	c.softLimit = 4
	for i := 0; i < 6; i++ {
		c.Add(string(rune('a' + i)))
	}
	if c.Len() > 6 {
		t.Fatalf("cache exceeded raw insert count: %d", c.Len())
	}
	// Forcefully expire everything by rewinding the timestamps, then
	// trigger another sweep via Add.
	for k := range c.seen {
		c.seen[k] = time.Now().Add(-2 * ReplayWindow)
	}
	c.Add("z")
	if c.Len() > 1 {
		t.Fatalf("sweep failed: Len=%d after aged-out adds", c.Len())
	}
}
