package webhook

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	q, err := OpenQueueAt(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("OpenQueueAt: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func TestQueue_EnqueuePeekAckRoundtrip(t *testing.T) {
	q := newTestQueue(t)
	key, err := q.Enqueue(Job{Deploy: "x", Event: "push", SHA: "abc"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if key == "" {
		t.Fatal("Enqueue must return a non-empty key")
	}
	got, err := q.Peek()
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got == nil || got.Deploy != "x" || got.SHA != "abc" {
		t.Fatalf("Peek = %+v, want deploy=x sha=abc", got)
	}
	if err := q.Claim(got.Key, time.Minute); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// While leased, Peek skips it.
	if next, _ := q.Peek(); next != nil {
		t.Fatalf("Peek must skip claimed job, got %+v", next)
	}
	if err := q.Ack(got.Key); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if next, _ := q.Peek(); next != nil {
		t.Fatalf("Peek after Ack must be empty, got %+v", next)
	}
}

func TestQueue_FailBackoffThenRequeue(t *testing.T) {
	q := newTestQueue(t)
	key, _ := q.Enqueue(Job{Deploy: "x", SHA: "abc"})
	_ = q.Claim(key, time.Minute)
	if _, err := q.Fail(key, "build error"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// First retry: NotBefore is now+30s — Peek must skip it for now.
	if got, _ := q.Peek(); got != nil {
		t.Fatalf("Peek must respect NotBefore backoff, got %+v", got)
	}
	queued, dead, err := q.Depth()
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if queued != 1 || dead != 0 {
		t.Fatalf("Depth = (%d,%d), want (1,0)", queued, dead)
	}
}

func TestQueue_DeadLetterAfterMaxRetries(t *testing.T) {
	q := newTestQueue(t)
	key, _ := q.Enqueue(Job{Deploy: "x", SHA: "abc"})
	// 6 failures → backoffFor returns -1 → dead-letter.
	var dlSeen bool
	for i := 0; i < 6; i++ {
		dl, err := q.Fail(key, "still broken")
		if err != nil {
			t.Fatalf("Fail #%d: %v", i, err)
		}
		if dl {
			dlSeen = true
		}
	}
	if !dlSeen {
		t.Fatal("expected one Fail to report deadLettered=true")
	}
	queued, dead, _ := q.Depth()
	if queued != 0 {
		t.Fatalf("after dead-letter, queued = %d, want 0", queued)
	}
	if dead != 1 {
		t.Fatalf("after dead-letter, dead = %d, want 1", dead)
	}
}

func TestQueue_SweepStaleLeases(t *testing.T) {
	q := newTestQueue(t)
	key, _ := q.Enqueue(Job{Deploy: "x"})
	_ = q.Claim(key, -1*time.Second) // already-expired lease
	if got, _ := q.Peek(); got == nil {
		t.Fatal("Peek must surface job with expired lease")
	}
	if err := q.SweepStaleLeases(); err != nil {
		t.Fatalf("SweepStaleLeases: %v", err)
	}
}

func TestQueue_PruneDeadLetters(t *testing.T) {
	q := newTestQueue(t)
	// Manually shove a job into the dead bucket by failing past max.
	key, _ := q.Enqueue(Job{Deploy: "x"})
	for i := 0; i < 6; i++ {
		_, _ = q.Fail(key, "doomed")
	}
	pruned, err := q.PruneDeadLetters(0) // any age qualifies
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	_, dead, _ := q.Depth()
	if dead != 0 {
		t.Fatalf("after prune dead = %d, want 0", dead)
	}
}

func TestQueue_TestAndAddViaReplayCache(t *testing.T) {
	// This test belongs more to replay_test but the queue + replay
	// interact at server level — quick sanity that TestAndAdd is a
	// single-lock op observable through a parallel test.
	c := NewReplayCache()
	if c.TestAndAdd("deliv-1") {
		t.Fatal("first TestAndAdd must return false")
	}
	if !c.TestAndAdd("deliv-1") {
		t.Fatal("second TestAndAdd must return true")
	}
	c.Forget("deliv-1")
	if c.TestAndAdd("deliv-1") {
		t.Fatal("after Forget, TestAndAdd must return false again")
	}
}
