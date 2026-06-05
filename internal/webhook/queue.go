package webhook

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

// QueueDBPath is where bbolt stores apigw's job queue. Per-host, owned by
// the webhook user (root via systemd). File mode 0600.
//
// We use bbolt (an etcd-vendored fork of Bolt) because: single-file, no
// daemon, ACID, native Go, used by Nomad, Tailscale, etcd itself. Doesn't
// add a deployment requirement (Redis, Postgres) for a single-host installer.
const QueueDBPath = "/var/lib/apigw/jobs.db"

var (
	bucketJobs  = []byte("jobs")  // key=monotonic, value=Job JSON
	bucketLease = []byte("lease") // key=jobkey, value=lease expiry (8 bytes BE unix nanos)
	bucketDead  = []byte("dead")  // permanently-failed jobs (>5 retries)
	bucketMeta  = []byte("meta")  // misc state: "next" → monotonic counter
)

// Job is one persisted unit of work. The webhook server enqueues these from
// validated HTTP requests; the worker dequeues and runs deploy.Apply.
//
// We store the original webhook envelope (Event, Delivery, RawBody) for
// auditability — `apigw webhook list --deliveries` and dashboard's webhook
// feed both read these.
type Job struct {
	Key        string    `json:"key"` // hex-encoded monotonic counter
	Deploy     string    `json:"deploy"`
	Event      string    `json:"event"`    // X-GitHub-Event
	Delivery   string    `json:"delivery"` // X-GitHub-Delivery
	Repo       string    `json:"repo"`     // pulled from payload
	Branch     string    `json:"branch"`   // pulled from payload (ref → branch)
	SHA        string    `json:"sha"`      // commit SHA, if known
	EnqueuedAt time.Time `json:"enqueued_at"`
	Retries    int       `json:"retries"`
	LastError  string    `json:"last_error,omitempty"`
	NotBefore  time.Time `json:"not_before,omitempty"` // backoff hint
}

// Queue is the bbolt-backed persistent job queue.
//
// Lifecycle: NewQueue → Enqueue from server, Peek/Claim/Ack/Fail from worker,
// Close on shutdown. All operations are crash-safe — losing power
// mid-Enqueue is fine because bbolt's Tx is fsync'd before returning.
type Queue struct {
	db *bbolt.DB
}

// OpenQueue opens (or creates) the queue at QueueDBPath. Idempotent;
// returns an error if the file is corrupted or another process holds the
// bbolt lock.
func OpenQueue() (*Queue, error) {
	return OpenQueueAt(QueueDBPath)
}

// OpenQueueAt is the explicit-path variant for tests.
func OpenQueueAt(path string) (*Queue, error) {
	if err := mkParent(path); err != nil {
		return nil, err
	}
	// 5 s timeout — if another apigw instance holds the lock we want a clear
	// error, not an indefinite hang.
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, b := range [][]byte{bucketJobs, bucketLease, bucketDead, bucketMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Queue{db: db}, nil
}

// Close flushes and releases the file lock. Safe to call repeatedly.
func (q *Queue) Close() error {
	if q.db == nil {
		return nil
	}
	return q.db.Close()
}

// Enqueue persists a new Job, assigning it a monotonic key. Returns the key
// so the caller can correlate with Stats.
func (q *Queue) Enqueue(j Job) (string, error) {
	if j.Deploy == "" {
		return "", errors.New("enqueue: deploy is required")
	}
	if j.EnqueuedAt.IsZero() {
		j.EnqueuedAt = time.Now().UTC()
	}
	var keyStr string
	err := q.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		jobs := tx.Bucket(bucketJobs)

		// Allocate next monotonic ID.
		seq, _ := jobs.NextSequence()
		key := encodeKey(seq)
		j.Key = fmt.Sprintf("%016x", seq)

		body, err := json.Marshal(j)
		if err != nil {
			return err
		}
		_ = meta // currently unused; reserved for cursor state if we add prefetching
		if err := jobs.Put(key, body); err != nil {
			return err
		}
		keyStr = j.Key
		return nil
	})
	return keyStr, err
}

// Peek returns the oldest non-claimed job whose NotBefore is in the past.
// Caller normally Claims it next.
//
// Returns (nil, nil) when the queue is empty or every job is either claimed
// (lease in the future) or NotBefore is in the future (backoff).
func (q *Queue) Peek() (*Job, error) {
	var out *Job
	now := time.Now().UTC()
	err := q.db.View(func(tx *bbolt.Tx) error {
		jobs := tx.Bucket(bucketJobs)
		leases := tx.Bucket(bucketLease)
		c := jobs.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Skip claimed (lease still valid).
			if leaseBytes := leases.Get(k); leaseBytes != nil {
				if expiry := decodeUnixNanos(leaseBytes); expiry.After(now) {
					continue
				}
			}
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				continue
			}
			if !j.NotBefore.IsZero() && j.NotBefore.After(now) {
				continue
			}
			out = &j
			return nil
		}
		return nil
	})
	return out, err
}

// Claim marks `key` as in-flight until `leaseFor` from now. A subsequent
// Peek won't return it. Use Ack on success, Fail on error.
//
// If a worker crashes mid-job, the lease expires and the next Peek
// re-surfaces it — "at-least-once" delivery.
func (q *Queue) Claim(key string, leaseFor time.Duration) error {
	k, err := keyBytes(key)
	if err != nil {
		return err
	}
	expiry := time.Now().UTC().Add(leaseFor)
	return q.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(bucketLease).Put(k, encodeUnixNanos(expiry))
	})
}

// Ack removes a job permanently. Call after successful Apply.
func (q *Queue) Ack(key string) error {
	k, err := keyBytes(key)
	if err != nil {
		return err
	}
	return q.db.Update(func(tx *bbolt.Tx) error {
		_ = tx.Bucket(bucketLease).Delete(k)
		return tx.Bucket(bucketJobs).Delete(k)
	})
}

// Fail records an error and either re-queues with backoff or dead-letters.
// Backoff: 30s, 2m, 10m, 30m, 2h. After 5 failures → bucketDead.
func (q *Queue) Fail(key string, errMsg string) error {
	k, err := keyBytes(key)
	if err != nil {
		return err
	}
	return q.db.Update(func(tx *bbolt.Tx) error {
		jobs := tx.Bucket(bucketJobs)
		body := jobs.Get(k)
		if body == nil {
			return nil // already acked or gone
		}
		var j Job
		if err := json.Unmarshal(body, &j); err != nil {
			return err
		}
		j.Retries++
		j.LastError = errMsg

		backoff := backoffFor(j.Retries)
		if backoff < 0 {
			// Dead-letter.
			dead := tx.Bucket(bucketDead)
			updated, _ := json.Marshal(j)
			if err := dead.Put(k, updated); err != nil {
				return err
			}
			_ = tx.Bucket(bucketLease).Delete(k)
			return jobs.Delete(k)
		}
		j.NotBefore = time.Now().UTC().Add(backoff)
		updated, _ := json.Marshal(j)
		if err := jobs.Put(k, updated); err != nil {
			return err
		}
		// Release the lease so Peek can see it again after NotBefore.
		return tx.Bucket(bucketLease).Delete(k)
	})
}

// SweepStaleLeases removes lease entries whose expiry has passed. Run
// periodically by the worker; cheap.
func (q *Queue) SweepStaleLeases() error {
	now := time.Now().UTC()
	return q.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketLease)
		c := b.Cursor()
		toDelete := [][]byte{}
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if decodeUnixNanos(v).Before(now) {
				toDelete = append(toDelete, append([]byte{}, k...))
			}
		}
		for _, k := range toDelete {
			_ = b.Delete(k)
		}
		return nil
	})
}

// Depth returns (queued, dead) — for `apigw webhook status`.
func (q *Queue) Depth() (queued, dead int, err error) {
	err = q.db.View(func(tx *bbolt.Tx) error {
		queued = tx.Bucket(bucketJobs).Stats().KeyN
		dead = tx.Bucket(bucketDead).Stats().KeyN
		return nil
	})
	return
}

// PruneDeadLetters deletes dead-letter entries older than `maxAge`. bbolt
// reuses freed pages but never shrinks the file — Compact() does that. Run
// Prune first, Compact second.
//
// Returns the number of entries removed.
func (q *Queue) PruneDeadLetters(maxAge time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	removed := 0
	err := q.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(bucketDead)
		c := b.Cursor()
		toDelete := [][]byte{}
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				// Corrupt entry — drop it.
				toDelete = append(toDelete, append([]byte{}, k...))
				continue
			}
			if j.EnqueuedAt.Before(cutoff) {
				toDelete = append(toDelete, append([]byte{}, k...))
			}
		}
		for _, k := range toDelete {
			_ = b.Delete(k)
			removed++
		}
		return nil
	})
	return removed, err
}

// Compact rewrites the bbolt file in-place, reclaiming pages freed by
// Delete operations. Returns the bytes saved (file size before − after).
//
// The dance: open a new DB at <path>.compacting, call bbolt.Compact() to
// copy live data, swap files atomically. The current Queue's *bbolt.DB
// stays open across the swap — bbolt's mmap is invalidated, so callers
// MUST not use the Queue concurrently with Compact. Worker schedules it
// during idle ticks.
func (q *Queue) Compact() (int64, error) {
	if q.db == nil {
		return 0, errors.New("queue closed")
	}
	src := q.db.Path()
	tmp := src + ".compacting"
	_ = os.Remove(tmp)

	dst, err := bbolt.Open(tmp, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return 0, fmt.Errorf("open tmp db: %w", err)
	}
	if err := bbolt.Compact(dst, q.db, 64*1024); err != nil {
		dst.Close()
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("compact: %w", err)
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := dst.Close(); err != nil {
		return 0, err
	}

	beforeStat, _ := os.Stat(src)
	if err := q.db.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, src); err != nil {
		// Reopen the original so callers don't have a dangling Queue.
		q.db, _ = bbolt.Open(src, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
		return 0, fmt.Errorf("rename: %w", err)
	}
	reopened, err := bbolt.Open(src, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return 0, fmt.Errorf("reopen after compact: %w", err)
	}
	q.db = reopened
	afterStat, _ := os.Stat(src)
	if beforeStat != nil && afterStat != nil {
		return beforeStat.Size() - afterStat.Size(), nil
	}
	return 0, nil
}

// RecentDeliveries returns up to `n` most-recent jobs (across queued + dead),
// newest first. Used by the dashboard's webhook activity feed.
func (q *Queue) RecentDeliveries(n int) ([]Job, error) {
	out := make([]Job, 0, n)
	err := q.db.View(func(tx *bbolt.Tx) error {
		for _, bucket := range [][]byte{bucketJobs, bucketDead} {
			c := tx.Bucket(bucket).Cursor()
			for k, v := c.Last(); k != nil && len(out) < n; k, v = c.Prev() {
				var j Job
				if err := json.Unmarshal(v, &j); err == nil {
					out = append(out, j)
				}
			}
		}
		return nil
	})
	return out, err
}

// backoffFor returns the delay before the n-th retry, or -1 for dead-letter.
//
//	1 → 30 s
//	2 → 2 m
//	3 → 10 m
//	4 → 30 m
//	5 → 2 h
//	6 → dead-letter (-1)
//
// Tuned so a deploy that fails for a transient reason (npm registry blip,
// GitHub clone hiccup) self-recovers within ~3 hours of human downtime.
func backoffFor(retries int) time.Duration {
	switch retries {
	case 1:
		return 30 * time.Second
	case 2:
		return 2 * time.Minute
	case 3:
		return 10 * time.Minute
	case 4:
		return 30 * time.Minute
	case 5:
		return 2 * time.Hour
	}
	return -1
}

func encodeKey(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

func keyBytes(hex string) ([]byte, error) {
	var seq uint64
	if _, err := fmt.Sscanf(hex, "%016x", &seq); err != nil {
		return nil, fmt.Errorf("invalid job key %q: %w", hex, err)
	}
	return encodeKey(seq), nil
}

func encodeUnixNanos(t time.Time) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(t.UnixNano()))
	return b
}

func decodeUnixNanos(b []byte) time.Time {
	if len(b) != 8 {
		return time.Time{}
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(b))).UTC()
}

func mkParent(path string) error {
	// os import would go circular with bbolt's internal os; use filepath
	// helpers + an inline implementation.
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return makeDir(dir, 0o755)
}
