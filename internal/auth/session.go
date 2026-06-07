// Cookie-based session store. The cookie value is the session ID —
// 32 bytes of `crypto/rand`, base64-url encoded — unguessable by
// design (2^256 search space). Nothing is encrypted into the cookie;
// everything lives in bbolt, which means revocation is instant (just
// delete the row).
//
// Why bbolt, not Redis or a JWT?
//   - Single static binary — no extra moving parts.
//   - Sessions are inherently stateful (revoke / list / sliding
//     window) so the stateless-JWT trade-off doesn't apply.
//   - Disk I/O for one bbolt lookup is on the order of a TLS
//     handshake; cheap.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// Session is one row in the store. Meta is for app-specific data the
// issuer wants to surface to downstream services (display name, plan
// tier, etc.) without round-tripping the IdP.
type Session struct {
	ID        string            `json:"id"`
	Subject   string            `json:"subject"`
	Scopes    []string          `json:"scopes,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	IssuedAt  time.Time         `json:"issued_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	LastSeen  time.Time         `json:"last_seen"`
}

// Expired reports whether the session is past ExpiresAt.
func (s *Session) Expired() bool { return time.Now().After(s.ExpiresAt) }

// SessionStore wraps a bbolt DB. One per process; concurrent-safe via
// bbolt's own lock + our mu around sliding-window LastSeen updates.
type SessionStore struct {
	db *bbolt.DB
	mu sync.Mutex
}

var sessionBucket = []byte("sessions")

// Errors callers may want to discriminate against.
var (
	ErrSessionNotFound = errors.New("session: not found")
	ErrSessionExpired  = errors.New("session: expired")
)

// OpenSessionStore opens / creates the bbolt DB at path. Parent dirs
// are created with 0o755; the file itself is 0o600.
func OpenSessionStore(path string) (*SessionStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open sessions.db %s: %w", path, err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(sessionBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init bucket: %w", err)
	}
	return &SessionStore{db: db}, nil
}

func (s *SessionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Issue mints a new session and writes it to the store. Returns the
// populated Session (including the random ID) so the caller can set
// the cookie. If ttl<=0 the call returns an error — defaulting would
// hide configuration mistakes.
func (s *SessionStore) Issue(subject string, scopes []string, meta map[string]string, ttl time.Duration) (*Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("session store not initialised")
	}
	if subject == "" {
		return nil, errors.New("session: subject required")
	}
	if ttl <= 0 {
		return nil, errors.New("session: ttl must be > 0")
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:        id,
		Subject:   subject,
		Scopes:    scopes,
		Meta:      meta,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		LastSeen:  now,
	}
	if err := s.put(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Lookup fetches the session by ID. Returns ErrSessionNotFound when
// no row exists and ErrSessionExpired when the row is past
// ExpiresAt (the expired row is left in place — Cleanup() removes
// stale rows in bulk).
//
// When slidingTTL > 0 a successful lookup extends ExpiresAt to
// now+slidingTTL atomically.
func (s *SessionStore) Lookup(id string, slidingTTL time.Duration) (*Session, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("session store not initialised")
	}
	if id == "" {
		return nil, ErrSessionNotFound
	}
	var sess Session
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return ErrSessionNotFound
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrSessionNotFound
		}
		return json.Unmarshal(raw, &sess)
	})
	if err != nil {
		return nil, err
	}
	if sess.Expired() {
		return &sess, ErrSessionExpired
	}
	if slidingTTL > 0 {
		s.mu.Lock()
		sess.LastSeen = time.Now().UTC()
		sess.ExpiresAt = sess.LastSeen.Add(slidingTTL)
		_ = s.put(&sess) // best-effort — Lookup must succeed even if write fails
		s.mu.Unlock()
	} else {
		// Always update LastSeen so list/audit shows activity.
		s.mu.Lock()
		sess.LastSeen = time.Now().UTC()
		_ = s.put(&sess)
		s.mu.Unlock()
	}
	return &sess, nil
}

// Revoke deletes the row. Idempotent — revoking an absent session
// returns nil.
func (s *SessionStore) Revoke(id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	})
}

// RevokeBySubject removes every session belonging to a subject.
// Returns the number deleted. Used on password rotation, account
// suspension, "log out everywhere" UX, etc.
func (s *SessionStore) RevokeBySubject(subject string) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	dropped := 0
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			var sess Session
			if json.Unmarshal(v, &sess) != nil {
				return nil
			}
			if sess.Subject == subject {
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

// List returns up to `limit` sessions, optionally filtered by
// subject. Set subject="" to list every session. Order is
// implementation-defined (bbolt key order = lexicographic on the
// random IDs = effectively random).
func (s *SessionStore) List(subject string, limit int) ([]*Session, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var out []*Session
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			if len(out) >= limit {
				return nil
			}
			var sess Session
			if json.Unmarshal(v, &sess) != nil {
				return nil
			}
			if subject != "" && sess.Subject != subject {
				return nil
			}
			out = append(out, &sess)
			return nil
		})
	})
	return out, err
}

// Cleanup deletes every session whose ExpiresAt is in the past.
// Safe to call from a periodic ticker.
func (s *SessionStore) Cleanup() (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	dropped := 0
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		now := time.Now()
		var toDelete [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			var sess Session
			if json.Unmarshal(v, &sess) != nil {
				toDelete = append(toDelete, append([]byte(nil), k...))
				return nil
			}
			if now.After(sess.ExpiresAt) {
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

// Size returns the total row count (fresh + stale). Cheap — bbolt
// keeps a per-bucket counter.
func (s *SessionStore) Size() int {
	if s == nil || s.db == nil {
		return 0
	}
	n := 0
	_ = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n
}

// ---------- helpers ----------

func (s *SessionStore) put(sess *Session) error {
	body, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(sessionBucket)
		if b == nil {
			return errors.New("session: bucket missing")
		}
		return b.Put([]byte(sess.ID), body)
	})
}

// newSessionID returns 32 bytes of crypto/rand encoded as URL-safe
// base64 (no padding). 256 bits of entropy — unguessable.
func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
