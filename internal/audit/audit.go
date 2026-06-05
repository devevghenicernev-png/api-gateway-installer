// Package audit is apigw's immutable, queryable, hash-chained audit log.
//
// Every operator mutation (deploy apply, api add, secret rotate, RBAC
// change, etc.) writes an Entry whose Hash includes the previous Entry's
// Hash. Tamper detection: re-hash any entry's window and any insertion or
// deletion breaks the chain.
//
// Storage: bbolt at <StateDir>/audit.db (already a dep — webhook queue
// uses it). One bucket "entries" keyed by uint64 monotonic ID.
//
// Query: streaming filter + decode. For SIEM export (`apigw audit export
// --format json-lines`), Entries serialize to one JSON object per line,
// compatible with Splunk HEC, ELK, Sumo, Datadog.
package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Entry is one audit record. JSON-tagged because we emit JSON-lines for
// SIEM ingestion. Fields are deliberately flat for query simplicity.
type Entry struct {
	ID        uint64            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Actor     string            `json:"actor"`              // username or system origin
	ActorIP   string            `json:"actor_ip,omitempty"` // when known
	Action    string            `json:"action"`             // dot-namespaced: deploy.apply, api.add, secret.rotate
	Resource  string            `json:"resource"`           // affected entity ID
	Result    string            `json:"result"`             // ok | failed | denied
	Reason    string            `json:"reason,omitempty"`   // when Result != ok
	Before    map[string]any    `json:"before,omitempty"`   // pre-change snapshot
	After     map[string]any    `json:"after,omitempty"`    // post-change snapshot
	Metadata  map[string]string `json:"metadata,omitempty"` // freeform tags
	PrevHash  string            `json:"prev_hash"`          // hash of preceding Entry; zero-string for first
	Hash      string            `json:"hash"`               // sha256 over the canonical Entry minus this field
}

// Logger is the package's public surface. One Logger per process is fine —
// all writes go through a single bbolt tx which serializes naturally.
type Logger struct {
	db       *bolt.DB
	mu       sync.Mutex
	nextID   uint64
	lastHash string
}

const (
	bucketName = "audit_entries"
	dbFileName = "audit.db"
)

// Open initializes the audit log at <dir>/audit.db. The bucket is created
// on first use and the chain tip (last hash + next ID) is restored on
// reopen.
func Open(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir audit dir: %w", err)
	}
	path := filepath.Join(dir, dbFileName)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open audit.db: %w", err)
	}
	l := &Logger{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	// Restore chain tip.
	_ = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		c := b.Cursor()
		if k, v := c.Last(); k != nil {
			l.nextID = decodeID(k) + 1
			var e Entry
			if err := json.Unmarshal(v, &e); err == nil {
				l.lastHash = e.Hash
			}
		}
		return nil
	})
	return l, nil
}

// Close releases the bbolt handle.
func (l *Logger) Close() error { return l.db.Close() }

// Log appends an Entry. Caller fills Actor/Action/Resource/Result and
// optionally Before/After/Metadata. We supply ID/Timestamp/PrevHash/Hash.
//
// Returns the recorded Entry so callers can include the ID in CLI output.
func (l *Logger) Log(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.ID = l.nextID
	e.Timestamp = time.Now().UTC()
	e.PrevHash = l.lastHash
	e.Hash = computeHash(e)

	body, err := json.Marshal(e)
	if err != nil {
		return e, fmt.Errorf("marshal audit entry: %w", err)
	}
	if err := l.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		return b.Put(encodeID(e.ID), body)
	}); err != nil {
		return e, fmt.Errorf("persist audit entry: %w", err)
	}
	l.nextID++
	l.lastHash = e.Hash
	return e, nil
}

// Filter narrows a Query.
type Filter struct {
	Since    time.Time
	Until    time.Time
	Actor    string
	Action   string
	Resource string
	Result   string
	Limit    int
}

// Query streams matching entries oldest→newest. Yields up to Filter.Limit
// (0 = no cap). Iteration stops if visit returns false.
func (l *Logger) Query(f Filter, visit func(Entry) bool) error {
	return l.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		c := b.Cursor()
		n := 0
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				continue
			}
			if !matches(e, f) {
				continue
			}
			if !visit(e) {
				return nil
			}
			n++
			if f.Limit > 0 && n >= f.Limit {
				return nil
			}
		}
		return nil
	})
}

// ExportJSONL writes all matching entries as JSON-lines to w. Suitable for
// `splunk hec`, `filebeat`, `vector` pickup.
func (l *Logger) ExportJSONL(f Filter, w io.Writer) (int, error) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	n := 0
	err := l.Query(f, func(e Entry) bool {
		if err := enc.Encode(e); err != nil {
			return false
		}
		n++
		return true
	})
	return n, err
}

// VerifyChain walks every entry and re-checks PrevHash + Hash. Returns
// the ID of the first mismatch, or 0 if the chain is intact.
//
// Slow (linear in entries) — intended for `apigw audit verify` and SIEM
// reconciliation jobs, not online use.
func (l *Logger) VerifyChain() (uint64, error) {
	var prev string
	var brokenID uint64
	err := l.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				brokenID = decodeID(k)
				return errors.New("decode failed")
			}
			if e.PrevHash != prev {
				brokenID = e.ID
				return errors.New("PrevHash mismatch")
			}
			expected := computeHash(e)
			if e.Hash != expected {
				brokenID = e.ID
				return errors.New("Hash mismatch")
			}
			prev = e.Hash
		}
		return nil
	})
	return brokenID, err
}

// Count returns the total entry count. Cheap — single bbolt tx.
func (l *Logger) Count() (uint64, error) {
	var n uint64
	err := l.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		n = uint64(b.Stats().KeyN)
		return nil
	})
	return n, err
}

// ---------- internals ----------

// computeHash returns the canonical Entry hash. The Entry's own Hash field
// is excluded from the digest (otherwise it'd be self-referential).
//
// Format: JSON of the entry with Hash="" — deterministic because Go's
// encoding/json sorts map keys.
func computeHash(e Entry) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func matches(e Entry, f Filter) bool {
	if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && e.Timestamp.After(f.Until) {
		return false
	}
	if f.Actor != "" && e.Actor != f.Actor {
		return false
	}
	if f.Action != "" && e.Action != f.Action {
		return false
	}
	if f.Resource != "" && e.Resource != f.Resource {
		return false
	}
	if f.Result != "" && e.Result != f.Result {
		return false
	}
	return true
}

func encodeID(id uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, id)
	return b
}

func decodeID(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
