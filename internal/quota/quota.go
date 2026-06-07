// Package quota implements per-consumer rolling-window request
// counters. The dashboard auth handlers call Charge() after a
// successful auth; Charge increments the consumer's counter for the
// current window and reports whether the cap has been crossed.
//
// Storage: bbolt at StateDir/quotas.db. One bucket per (consumer,
// window-size); keys are unix-floor-to-window-start; values are
// little-endian uint64 counters.
//
// Window granularity: any time.Duration ≥ 1m. The store rounds the
// "now" timestamp down to the nearest multiple of the window so
// keys are deterministic and sliding windows degenerate to fixed-
// interval buckets (which is what every gateway actually
// implements — true sliding windows need per-request timestamps).
package quota

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// Limit declares one cap. Window is how long the rolling counter
// spans; Max is the request budget for that window.
type Limit struct {
	Window time.Duration
	Max    int64
}

// Decision is what Charge returns.
type Decision struct {
	Allowed   bool
	Remaining int64 // remaining requests in this window (≥0)
	ResetAt   time.Time
	Limit     int64
}

// Store wraps the bbolt DB. Concurrent-safe (bbolt's own lock +
// our mu for the read-then-write Charge sequence).
type Store struct {
	mu sync.Mutex
	db *bbolt.DB
}

var bucketCounters = []byte("counters")

// Open creates or opens the quota store. path is typically
// StateDir/quotas.db.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open quotas.db %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketCounters)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init bucket: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Charge atomically increments the counter for (consumerID, window)
// at the current bucket and decides whether the request is allowed.
//
// When limit.Max is ≤0 quotas are off — Allowed=true, Remaining=-1.
// When the counter would exceed the cap, Allowed=false and the
// counter is NOT incremented (so the next attempt sees the same
// state until the window rolls).
func (s *Store) Charge(consumerID string, limit Limit, now time.Time) (Decision, error) {
	if s == nil || s.db == nil {
		return Decision{}, errors.New("quota store not initialised")
	}
	if limit.Max <= 0 || limit.Window <= 0 {
		return Decision{Allowed: true, Remaining: -1, Limit: limit.Max}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucketStart := now.Truncate(limit.Window)
	resetAt := bucketStart.Add(limit.Window)
	key := keyFor(consumerID, limit.Window, bucketStart)

	var current int64
	allowed := false
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketCounters)
		if b == nil {
			return errors.New("quota: bucket missing")
		}
		if raw := b.Get(key); len(raw) == 8 {
			current = int64(binary.BigEndian.Uint64(raw))
		}
		if current >= limit.Max {
			allowed = false
			return nil
		}
		current++
		allowed = true
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(current))
		return b.Put(key, buf)
	})
	if err != nil {
		return Decision{}, err
	}
	remaining := limit.Max - current
	if remaining < 0 {
		remaining = 0
	}
	return Decision{
		Allowed:   allowed,
		Remaining: remaining,
		ResetAt:   resetAt,
		Limit:     limit.Max,
	}, nil
}

// Peek reports the current counter + decision without incrementing.
// Useful for /api/admin/quota inspectors.
func (s *Store) Peek(consumerID string, limit Limit, now time.Time) (Decision, error) {
	if s == nil || s.db == nil {
		return Decision{}, errors.New("quota store not initialised")
	}
	if limit.Max <= 0 || limit.Window <= 0 {
		return Decision{Allowed: true, Remaining: -1, Limit: limit.Max}, nil
	}
	bucketStart := now.Truncate(limit.Window)
	resetAt := bucketStart.Add(limit.Window)
	key := keyFor(consumerID, limit.Window, bucketStart)
	var current int64
	_ = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketCounters)
		if b == nil {
			return nil
		}
		if raw := b.Get(key); len(raw) == 8 {
			current = int64(binary.BigEndian.Uint64(raw))
		}
		return nil
	})
	remaining := limit.Max - current
	if remaining < 0 {
		remaining = 0
	}
	return Decision{
		Allowed:   current < limit.Max,
		Remaining: remaining,
		ResetAt:   resetAt,
		Limit:     limit.Max,
	}, nil
}

// Cleanup removes counter rows whose window has already passed.
// Called from a periodic ticker — cheap (bbolt walks an in-memory
// B+ tree).
func (s *Store) Cleanup(now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	dropped := 0
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketCounters)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		_ = b.ForEach(func(k, _ []byte) error {
			if len(k) < 17 { // 8 bytes window + 8 bytes start + 1+ consumer
				return nil
			}
			window := time.Duration(binary.BigEndian.Uint64(k[:8]))
			start := time.Unix(int64(binary.BigEndian.Uint64(k[8:16])), 0)
			if start.Add(window).Before(now) {
				toDelete = append(toDelete, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		dropped = len(toDelete)
		return nil
	})
	return dropped, err
}

// keyFor builds a deterministic bbolt key: window(8) | windowStart
// unix(8) | consumerID. Encoding the window in the key lets one
// consumer have multiple parallel quotas (e.g. 10/sec AND 10000/day)
// without colliding.
func keyFor(consumerID string, window time.Duration, start time.Time) []byte {
	out := make([]byte, 16+len(consumerID))
	binary.BigEndian.PutUint64(out[:8], uint64(window))
	binary.BigEndian.PutUint64(out[8:16], uint64(start.Unix()))
	copy(out[16:], consumerID)
	return out
}
