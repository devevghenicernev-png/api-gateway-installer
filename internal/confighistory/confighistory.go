// Package confighistory keeps the last N config snapshots in a
// bbolt-backed ring buffer so operators can roll back to any prior
// generation. Hook: every cfg.Save() landing place can call
// Snapshot(currentBytes) before writing.
//
// Storage: bbolt at StateDir/config_history.db. One bucket
// `snapshots` keyed by monotonic generation id (uint64 big-endian);
// values are the raw config YAML bytes + a tiny header
// (timestamp, optional actor + reason).
package confighistory

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// DefaultCapacity is how many snapshots we retain. Old entries are
// purged on Snapshot(); pick large enough that a missed rollback
// window doesn't lose history, small enough that the bbolt file
// stays small.
const DefaultCapacity = 50

// Snapshot is one historical config generation. The Body is the raw
// YAML the operator wrote to /etc/apigw/config.yaml at the time.
type Snapshot struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Body      []byte    `json:"body"`
}

// Store wraps a bbolt DB.
type Store struct {
	mu       sync.Mutex
	db       *bbolt.DB
	capacity int
}

var (
	bucketSnapshots = []byte("snapshots")
	bucketMeta      = []byte("meta")
	keyNextID       = []byte("next_id")
)

// Open creates / opens the history DB.
func Open(path string) (*Store, error) {
	return OpenWithCapacity(path, DefaultCapacity)
}

// OpenWithCapacity lets tests use a smaller ring.
func OpenWithCapacity(path string, capacity int) (*Store, error) {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, b := range [][]byte{bucketSnapshots, bucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, capacity: capacity}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Snapshot records the new generation. Old rows beyond capacity are
// dropped. The returned ID is the new generation number — handy if
// the caller wants to print "saved generation N".
func (s *Store) Snapshot(body []byte, actor, reason string, now time.Time) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("history not initialised")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var id uint64
	err := s.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		if raw := meta.Get(keyNextID); len(raw) == 8 {
			id = binary.BigEndian.Uint64(raw)
		}
		id++
		// Write the snapshot.
		snap := Snapshot{ID: id, Timestamp: now.UTC(), Actor: actor, Reason: reason, Body: body}
		encoded, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		b := tx.Bucket(bucketSnapshots)
		if err := b.Put(uint64Key(id), encoded); err != nil {
			return err
		}
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, id)
		if err := meta.Put(keyNextID, buf); err != nil {
			return err
		}
		// Trim — keep the most-recent `capacity` rows.
		return trim(b, s.capacity)
	})
	return id, err
}

// List returns the snapshots in newest-first order, up to `limit`
// (0 = all). The Body field is NOT included; use Get for the bytes.
func (s *Store) List(limit int) ([]Snapshot, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var out []Snapshot
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			if limit > 0 && len(out) >= limit {
				break
			}
			var snap Snapshot
			if err := json.Unmarshal(v, &snap); err != nil {
				continue
			}
			snap.Body = nil // omit from listing
			out = append(out, snap)
		}
		return nil
	})
	return out, err
}

// Get returns one snapshot (Body included).
func (s *Store) Get(id uint64) (*Snapshot, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("history not initialised")
	}
	var snap Snapshot
	hit := false
	err := s.db.View(func(tx *bbolt.Tx) error {
		raw := tx.Bucket(bucketSnapshots).Get(uint64Key(id))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			return err
		}
		hit = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hit {
		return nil, fmt.Errorf("generation %d not found", id)
	}
	return &snap, nil
}

// Size returns the number of snapshots currently stored.
func (s *Store) Size() int {
	if s == nil || s.db == nil {
		return 0
	}
	n := 0
	_ = s.db.View(func(tx *bbolt.Tx) error {
		n = tx.Bucket(bucketSnapshots).Stats().KeyN
		return nil
	})
	return n
}

// trim keeps only the highest `capacity` rows in the snapshots
// bucket. Cheap — bbolt cursor walk. We count via ForEach rather
// than Stats().KeyN because the latter can lag inside the same
// Update tx that just inserted a key.
func trim(b *bbolt.Bucket, capacity int) error {
	if capacity <= 0 {
		return nil
	}
	count := 0
	_ = b.ForEach(func(_, _ []byte) error { count++; return nil })
	if count <= capacity {
		return nil
	}
	toRemove := count - capacity
	// Collect the oldest `toRemove` keys, then delete (don't mutate
	// during ForEach — cursor invalidation).
	var victims [][]byte
	_ = b.ForEach(func(k, _ []byte) error {
		if len(victims) >= toRemove {
			return nil
		}
		victims = append(victims, append([]byte(nil), k...))
		return nil
	})
	for _, k := range victims {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func uint64Key(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}
