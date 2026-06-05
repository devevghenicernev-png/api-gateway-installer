package events

// ring is a fixed-capacity, single-producer single-consumer-friendly ring
// buffer of Events. Reads under Hub.mu.RLock; writes under Hub.mu.Lock —
// no separate mutex needed.
//
// We hand-rolled this (~30 LoC) because Workiva/go-datastructures is
// overkill for ~10 topics × 1024 capacity ≈ 10K event slots resident.
type ring struct {
	buf   []Event
	cap   int
	head  int // index of the OLDEST item; we overwrite at head when full
	count int
}

func newRing(cap int) *ring {
	if cap <= 0 {
		cap = 1024
	}
	return &ring{buf: make([]Event, cap), cap: cap}
}

// push appends e, overwriting the oldest entry if full.
func (r *ring) push(e Event) {
	if r.count < r.cap {
		idx := (r.head + r.count) % r.cap
		r.buf[idx] = e
		r.count++
		return
	}
	// Full: overwrite at head, advance head.
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
}

// since returns all stored events with ID > sinceID, in chronological order.
// Allocates a slice — caller-owned.
//
// Special case: sinceID == 0 returns everything currently buffered.
func (r *ring) since(sinceID uint64) []Event {
	out := make([]Event, 0, r.count)
	for i := 0; i < r.count; i++ {
		e := r.buf[(r.head+i)%r.cap]
		if sinceID == 0 || e.ID > sinceID {
			out = append(out, e)
		}
	}
	return out
}

// all returns the full buffer in chronological order.
func (r *ring) all() []Event {
	return r.since(0)
}
