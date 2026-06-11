// Package events is apigw's in-process pub/sub: the single hub that fans
// build logs, systemd state changes, webhook activity, and TLS expiry out
// to both the dashboard (over SSE) and the CLI (--follow, --watch).
//
// Why we hand-rolled it instead of pulling in Watermill or cskr/pubsub —
// see ARCHITECTURE.md §"The hub". TL;DR: Watermill is Kafka-shaped (heavy);
// cskr/pubsub lacks monotonic IDs needed for Last-Event-ID replay. Ours is
// ~120 LoC and exact-fit.
//
// Wire-level contract (Event.ID monotonic; Topic string; Data is a single
// JSON line because EventSource joins multi-line `data:` fields with \n)
// is documented in DESIGN.md §5c.
package events

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one published unit. All fields are immutable after Publish.
//
// ID is monotonic per Hub (atomic counter). Subscribers stash it as
// `Last-Event-ID` and replay-from on reconnect — that's how the dashboard
// survives Wi-Fi blips without losing a single line.
type Event struct {
	ID    uint64 `json:"id"`
	Topic string `json:"topic"`
	Type  string `json:"type"` // "stdout","state","webhook",...
	Ts    int64  `json:"ts"`   // unix nanos
	Data  []byte `json:"data"` // pre-encoded single-line JSON
}

// Hub is the central fan-out. Cheap to construct; New() once at process
// start, share via DI.
//
// Concurrency model:
//   - Publish is lock-free on the hot path until it walks the subscriber set.
//   - Per-subscriber bounded channel (cap=256). Slow subscriber gets the
//     drop-oldest policy: pop their queue's head, push the new event,
//     increment the drop counter. After DropDisconnectThreshold cumulative
//     drops we close the channel — the subscriber must reconnect and replay.
//   - Per-topic ring buffer (cap=1024). Late subscribers replay from there
//     instead of starting at "now".
type Hub struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	rings  map[string]*ring
	nextID atomic.Uint64

	// Metrics is an optional Prometheus sink — Inc()'d once per Publish
	// call, labeled by topic. nil is safe (no-op).
	Metrics PublishCounter

	// Tunables — exported for tests, defaulted in New().
	RingCap                 int
	SubChanCap              int
	DropDisconnectThreshold uint64
}

// PublishCounter is the minimal surface Hub depends on for instrumentation.
// Keeps internal/metrics out of internal/events's import graph.
type PublishCounter interface {
	IncEventsPublished(topic string)
}

// New returns an empty Hub with default sizing (ring=1024, sub-chan=256,
// drop-disconnect=1024).
func New() *Hub {
	return &Hub{
		subs:                    make(map[*Subscription]struct{}, 16),
		rings:                   make(map[string]*ring, 8),
		RingCap:                 1024,
		SubChanCap:              256,
		DropDisconnectThreshold: 1024,
	}
}

// Publish fans `data` to every subscriber registered for `topic`. Returns
// the assigned Event.ID so the caller can correlate (e.g. for the deploy
// state machine that wants to log "wrote ID 4218").
//
// `data` MUST be a single JSON line (no embedded newlines) so the SSE wire
// format stays parseable on the browser side.
func (h *Hub) Publish(topic, typ string, data []byte) uint64 {
	e := Event{
		ID:    h.nextID.Add(1),
		Topic: topic,
		Type:  typ,
		Ts:    time.Now().UnixNano(),
		Data:  data,
	}

	h.mu.Lock()
	r, ok := h.rings[topic]
	if !ok {
		r = newRing(h.RingCap)
		h.rings[topic] = r
	}
	r.push(e)
	if h.Metrics != nil {
		h.Metrics.IncEventsPublished(topic)
	}

	for sub := range h.subs {
		if !sub.wants(topic) {
			continue
		}
		deliver(sub, e)
	}
	h.mu.Unlock()
	return e.ID
}

// Subscription is the consumer-side handle. Ch is the event channel;
// close it via Cancel(). Drops reports cumulative dropped count.
//
// Topics may contain `*` wildcards matching a single dot-delimited segment,
// e.g. `deploy.*.stdout` matches `deploy.foodmanager.stdout` but not
// `deploy.foo.bar.stdout`. The dashboard relies on this to follow every
// deploy's build output without enumerating names. Non-wildcard topics stay
// in an O(1) exact-match set; only patterns containing `*` walk the slice.
type Subscription struct {
	exact    map[string]struct{}
	patterns []string
	ch       chan Event
	drops    atomic.Uint64
	closed   atomic.Bool
}

// wants reports whether this subscription is interested in `topic` — exact
// match first (O(1)), then any wildcard pattern.
func (s *Subscription) wants(topic string) bool {
	if _, ok := s.exact[topic]; ok {
		return true
	}
	for _, p := range s.patterns {
		if topicMatch(p, topic) {
			return true
		}
	}
	return false
}

// Ch returns the channel events are delivered on. Closed when the
// subscription is cancelled OR when DropDisconnectThreshold is exceeded.
func (s *Subscription) Ch() <-chan Event { return s.ch }

// Drops returns how many events have been dropped for this subscription
// since it was created. Surfaced to the dashboard as a banner so the
// operator knows "you missed N lines".
func (s *Subscription) Drops() uint64 { return s.drops.Load() }

// Subscribe registers a new consumer interested in `topics`. If sinceID > 0
// the hub first delivers any ring-buffered events with ID > sinceID before
// going live — that's the Last-Event-ID replay path.
//
// Returns the Subscription and a cancel func. Always defer cancel() in the
// caller.
func (h *Hub) Subscribe(topics []string, sinceID uint64) (*Subscription, func()) {
	exact, patterns := classify(topics)
	s := &Subscription{
		exact:    exact,
		patterns: patterns,
		ch:       make(chan Event, h.SubChanCap),
	}

	h.mu.Lock()
	// Replay first, while holding the lock — guarantees no gap between
	// "what was in the ring" and "what's live". With wildcards a single
	// pattern can hit multiple rings, so collect all matching events and
	// replay them in ID order (chronological) rather than per-topic.
	var replay []Event
	for t, r := range h.rings {
		if !s.wants(t) {
			continue
		}
		replay = append(replay, r.since(sinceID)...)
	}
	sortByIDAsc(replay)
	for _, e := range replay {
		select {
		case s.ch <- e:
		default:
			// Sub's channel is already full from a giant backlog;
			// fall through, the live loop will drop-oldest.
			s.drops.Add(1)
		}
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()

	return s, func() {
		if !s.closed.CompareAndSwap(false, true) {
			return
		}
		h.mu.Lock()
		delete(h.subs, s)
		h.mu.Unlock()
		close(s.ch)
	}
}

// Snapshot returns up to `n` most-recent events across `topics` (newest
// first). Used by the dashboard's "/api/logs/<deploy>?lines=N" historical
// endpoint and by `apigw deploy logs --lines N` initial dump.
func (h *Hub) Snapshot(topics []string, n int) []Event {
	exact, patterns := classify(topics)
	want := &Subscription{exact: exact, patterns: patterns}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Event, 0, n)
	for t, r := range h.rings {
		if !want.wants(t) {
			continue
		}
		out = append(out, r.all()...)
	}
	// Sort by ID descending, trim to n.
	sortByIDDesc(out)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// deliver implements drop-oldest backpressure. Caller holds h.mu.
func deliver(s *Subscription, e Event) {
	if s.closed.Load() {
		return
	}
	select {
	case s.ch <- e:
		return
	default:
	}
	// Drop the oldest queued event and try again. If we exceed the
	// disconnect threshold, close the channel — subscriber must reconnect.
	select {
	case <-s.ch:
	default:
	}
	select {
	case s.ch <- e:
		if s.drops.Add(1) > 1024 { // matches DropDisconnectThreshold default
			// Mark closed; the cancel func from Subscribe will be a no-op.
			// The next ID-gap on the client → reconnect path runs.
			if s.closed.CompareAndSwap(false, true) {
				close(s.ch)
			}
		}
	default:
		s.drops.Add(1)
	}
}

// classify splits requested topics into an exact-match set and a slice of
// wildcard patterns (those containing `*`). Keeping them apart lets the hot
// delivery path do an O(1) map lookup before falling back to pattern walks.
func classify(topics []string) (map[string]struct{}, []string) {
	exact := make(map[string]struct{}, len(topics))
	var patterns []string
	for _, t := range topics {
		if strings.IndexByte(t, '*') >= 0 {
			patterns = append(patterns, t)
			continue
		}
		exact[t] = struct{}{}
	}
	return exact, patterns
}

// topicMatch reports whether `topic` matches `pattern`, where a `*` segment
// in the pattern matches exactly one dot-delimited segment of the topic.
// Both sides must have the same number of segments. `deploy.*.stdout`
// matches `deploy.foo.stdout`, not `deploy.a.b.stdout`.
func topicMatch(pattern, topic string) bool {
	for {
		pi := strings.IndexByte(pattern, '.')
		ti := strings.IndexByte(topic, '.')
		if pi < 0 || ti < 0 {
			// Last segment on at least one side.
			if pi >= 0 || ti >= 0 {
				return false // segment counts differ
			}
			return pattern == "*" || pattern == topic
		}
		pseg, tseg := pattern[:pi], topic[:ti]
		if pseg != "*" && pseg != tseg {
			return false
		}
		pattern, topic = pattern[pi+1:], topic[ti+1:]
	}
}

// sortByIDDesc / sortByIDAsc are tiny in-place insertion sorts — avoid
// pulling in sort + a closure for the only call sites.
func sortByIDDesc(s []Event) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].ID < s[j].ID; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func sortByIDAsc(s []Event) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].ID > s[j].ID; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
