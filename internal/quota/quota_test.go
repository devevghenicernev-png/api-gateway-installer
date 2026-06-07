package quota

import (
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "quotas.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCharge_AllowedUnderCap(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	lim := Limit{Window: 1 * time.Minute, Max: 3}
	for i := int64(0); i < 3; i++ {
		d, err := s.Charge("alice", lim, now)
		if err != nil {
			t.Fatalf("charge %d: %v", i, err)
		}
		if !d.Allowed {
			t.Errorf("charge %d: want allowed", i)
		}
	}
	// Fourth call hits the cap.
	d, err := s.Charge("alice", lim, now)
	if err != nil {
		t.Fatalf("charge 4: %v", err)
	}
	if d.Allowed {
		t.Errorf("charge 4: want denied (cap exceeded)")
	}
	if d.Remaining != 0 {
		t.Errorf("remaining: got %d want 0", d.Remaining)
	}
}

func TestCharge_DeniedDoesNotIncrement(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	lim := Limit{Window: 1 * time.Minute, Max: 1}

	if d, _ := s.Charge("alice", lim, now); !d.Allowed {
		t.Fatalf("first should pass")
	}
	// Repeatedly hitting after cap doesn't change state.
	for i := 0; i < 5; i++ {
		d, _ := s.Charge("alice", lim, now)
		if d.Allowed {
			t.Errorf("retry %d: should stay denied", i)
		}
	}
	// Peek confirms counter = 1 (only the first charge incremented).
	p, _ := s.Peek("alice", lim, now)
	if p.Remaining != 0 || p.Limit != 1 {
		t.Errorf("peek state: %+v", p)
	}
}

func TestCharge_WindowRolls(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lim := Limit{Window: 1 * time.Minute, Max: 1}

	if d, _ := s.Charge("alice", lim, now); !d.Allowed {
		t.Fatalf("first should pass")
	}
	if d, _ := s.Charge("alice", lim, now); d.Allowed {
		t.Fatalf("second in same window: want denied")
	}
	// Next window — same consumer should be allowed again.
	later := now.Add(1 * time.Minute)
	if d, _ := s.Charge("alice", lim, later); !d.Allowed {
		t.Errorf("new window: want allowed")
	}
}

func TestCharge_PerConsumerIsolation(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	lim := Limit{Window: 1 * time.Minute, Max: 1}

	if d, _ := s.Charge("alice", lim, now); !d.Allowed {
		t.Fatalf("alice 1st")
	}
	if d, _ := s.Charge("alice", lim, now); d.Allowed {
		t.Fatalf("alice 2nd: should be denied")
	}
	// Bob untouched by alice's quota.
	if d, _ := s.Charge("bob", lim, now); !d.Allowed {
		t.Errorf("bob shouldn't share alice's bucket")
	}
}

func TestCharge_MultipleLimitsPerConsumer(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	short := Limit{Window: 1 * time.Second, Max: 2}
	long := Limit{Window: 1 * time.Hour, Max: 100}

	// Charge short twice — within both caps.
	for i := 0; i < 2; i++ {
		if d, _ := s.Charge("alice", short, now); !d.Allowed {
			t.Errorf("short %d: want allowed", i)
		}
		if d, _ := s.Charge("alice", long, now); !d.Allowed {
			t.Errorf("long %d: want allowed", i)
		}
	}
	// Third short hit denied.
	if d, _ := s.Charge("alice", short, now); d.Allowed {
		t.Errorf("short 3rd: want denied")
	}
	// Long limit still has headroom.
	if d, _ := s.Charge("alice", long, now); !d.Allowed {
		t.Errorf("long 3rd: should still be allowed")
	}
}

func TestCharge_ZeroLimitMeansOff(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 100; i++ {
		d, err := s.Charge("alice", Limit{Window: 1 * time.Minute, Max: 0}, time.Now())
		if err != nil || !d.Allowed {
			t.Errorf("max=0 should always allow: err=%v allowed=%v", err, d.Allowed)
		}
	}
}

func TestPeek_DoesNotIncrement(t *testing.T) {
	s := newStore(t)
	lim := Limit{Window: 1 * time.Minute, Max: 1}
	_, _ = s.Charge("alice", lim, time.Now())
	// Peek 10 times — counter must stay at 1.
	for i := 0; i < 10; i++ {
		p, _ := s.Peek("alice", lim, time.Now())
		if p.Remaining != 0 {
			t.Errorf("peek %d: changed counter (remaining=%d)", i, p.Remaining)
		}
	}
}

func TestCleanup_DropsPastWindows(t *testing.T) {
	s := newStore(t)
	old := time.Now().Add(-2 * time.Hour)
	_, _ = s.Charge("alice", Limit{Window: 1 * time.Minute, Max: 10}, old)
	_, _ = s.Charge("bob", Limit{Window: 1 * time.Minute, Max: 10}, time.Now())
	n, err := s.Cleanup(time.Now())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 1 {
		t.Errorf("dropped %d; want 1 (the old alice row)", n)
	}
}

func TestStore_NilSafe(t *testing.T) {
	var s *Store
	if _, err := s.Charge("a", Limit{Window: time.Minute, Max: 1}, time.Now()); err == nil {
		t.Errorf("nil Charge should error")
	}
	if err := s.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
	if n, _ := s.Cleanup(time.Now()); n != 0 {
		t.Errorf("nil Cleanup: %d", n)
	}
}
