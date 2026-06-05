package events

import "testing"

// TestRing_PushSinceWithinCap covers the linear case before the ring wraps:
// every event since `sinceID` should be returned in insertion order.
func TestRing_PushSinceWithinCap(t *testing.T) {
	r := newRing(8)
	for i := uint64(1); i <= 5; i++ {
		r.push(Event{ID: i})
	}
	got := r.since(2)
	if len(got) != 3 {
		t.Fatalf("since(2) should return 3 events; got %d", len(got))
	}
	for i, e := range got {
		want := uint64(i + 3)
		if e.ID != want {
			t.Fatalf("since(2)[%d].ID = %d, want %d", i, e.ID, want)
		}
	}
}

// TestRing_PushWrap pushes more than the capacity and verifies oldest
// items get overwritten while ordering is preserved.
func TestRing_PushWrap(t *testing.T) {
	r := newRing(3)
	for i := uint64(1); i <= 5; i++ {
		r.push(Event{ID: i})
	}
	got := r.all()
	if len(got) != 3 {
		t.Fatalf("expected 3 events resident; got %d", len(got))
	}
	want := []uint64{3, 4, 5}
	for i, e := range got {
		if e.ID != want[i] {
			t.Fatalf("all()[%d].ID = %d, want %d", i, e.ID, want[i])
		}
	}
}

// TestRing_SinceFromZeroReturnsAll — the SSE handler relies on this when
// the client never sent a Last-Event-ID.
func TestRing_SinceFromZeroReturnsAll(t *testing.T) {
	r := newRing(4)
	r.push(Event{ID: 10})
	r.push(Event{ID: 11})
	got := r.since(0)
	if len(got) != 2 {
		t.Fatalf("since(0) should return everything; got %d", len(got))
	}
}

// TestRing_SinceBeyondHead — a stale Last-Event-ID newer than anything in
// the ring (after server restart) must return an empty slice, not panic
// or return spurious data.
func TestRing_SinceBeyondHead(t *testing.T) {
	r := newRing(2)
	r.push(Event{ID: 1})
	got := r.since(99)
	if len(got) != 0 {
		t.Fatalf("since(99) on small ring should return 0; got %d", len(got))
	}
}
