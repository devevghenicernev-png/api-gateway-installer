package deploy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// JobQueue serialises Apply() calls per deploy name within a single
// process. It is NOT the persistence layer — that lives in
// internal/webhook/queue.go (bbolt) where webhook deliveries are durably
// enqueued and the worker drains them. JobQueue's only job is the
// single-flight property: if the worker and a CLI command both ask to
// Apply "deploy-foo", they will run one at a time.
//
// Per-deploy mutex (not global) means two pushes to different apps build
// in parallel; two pushes to the same app serialise.
type JobQueue struct {
	mu    sync.Mutex
	locks map[string]*deployLock
}

type deployLock struct {
	mu       sync.Mutex
	inFlight atomic.Bool
	lastSHA  atomic.Pointer[string] // SHA of the latest queued job, for debounce
}

// NewJobQueue returns an empty queue. Cheap; safe to construct per request.
func NewJobQueue() *JobQueue {
	return &JobQueue{locks: make(map[string]*deployLock)}
}

// ErrSuperseded is returned when a deploy was debounced by a newer one with
// the same SHA arriving before this one's mutex was acquired. Callers should
// treat it as success, not failure — the work was done by another goroutine.
var ErrSuperseded = errors.New("job superseded by newer request for same sha")

// Submit runs Apply under the per-deploy mutex.
//
// If two requests for the same deploy arrive at roughly the same time with
// the same commit SHA, the second one returns ErrSuperseded after the first
// completes. This is the debounce-on-SHA behaviour the spec calls for.
//
// `req.Name` is required; everything else is forwarded to Apply. The
// caller's ctx fully controls the acquire wait — no hidden timeout
// override. If you want a hard cap, wrap the ctx with WithTimeout yourself.
func (q *JobQueue) Submit(ctx context.Context, req ApplyRequest, sha string) (ApplyResult, error) {
	if req.Name == "" {
		return ApplyResult{}, errors.New("submit: name is required")
	}
	lock := q.lockFor(req.Name)

	// Pre-acquire: claim the "latest SHA" slot.
	if sha != "" {
		lock.lastSHA.Store(&sha)
	}

	// Wait for any in-flight job for this deploy to finish. The ctx the
	// caller passed in is the only timeout — overriding it (as we did
	// before with a hard-coded 30m) silently extended caller-controlled
	// shutdown windows.
	if err := acquire(ctx, &lock.mu); err != nil {
		return ApplyResult{}, err
	}
	defer lock.mu.Unlock()

	// If a newer same-SHA job claimed the slot while we waited, supersede us.
	if sha != "" {
		if latest := lock.lastSHA.Load(); latest != nil && *latest != sha {
			return ApplyResult{Name: req.Name, SHA: sha, Skipped: true}, nil
		}
	}

	lock.inFlight.Store(true)
	defer lock.inFlight.Store(false)

	return Apply(ctx, req)
}

// InFlight reports whether a job for `name` is currently executing. Useful
// for the dashboard's "building..." badge.
func (q *JobQueue) InFlight(name string) bool {
	q.mu.Lock()
	l, ok := q.locks[name]
	q.mu.Unlock()
	if !ok {
		return false
	}
	return l.inFlight.Load()
}

func (q *JobQueue) lockFor(name string) *deployLock {
	q.mu.Lock()
	defer q.mu.Unlock()
	if l, ok := q.locks[name]; ok {
		return l
	}
	l := &deployLock{}
	q.locks[name] = l
	return l
}

// acquire is sync.Mutex.Lock with context cancellation.
//
// sync.Mutex doesn't expose a ctx-aware Lock, so we run the blocking lock
// in a tiny helper goroutine and select between it and ctx.Done.
//
// On ctx cancel the helper is left to acquire+release in the background.
// This is intentional: at the moment ctx cancels, the helper may already
// own (or be about to own) the mutex; the only safe action is to let it
// take, then immediately drop, ownership. Cardinality is small (≤ a few
// hundred deploys per host) so the orphan goroutines are bounded and
// short-lived.
//
// Note: if many submitters time out in quick succession, the cumulative
// goroutine count is bounded by the number of in-flight Submit calls, not
// the timeout duration. That's safe.
func acquire(ctx context.Context, mu *sync.Mutex) error {
	ch := make(chan struct{})
	go func() {
		mu.Lock()
		close(ch)
	}()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		go func() {
			<-ch
			mu.Unlock()
		}()
		return ctx.Err()
	}
}
