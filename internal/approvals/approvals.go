// Package approvals implements change-request workflows: a mutation that
// would normally apply immediately is instead parked pending approval by
// N other operators. Required by SOX, PCI, change-management policies.
//
// Pattern: a CLI command that's gated by policy ("prod APIs need 2
// approvers") calls Submit() instead of applying. The returned ChangeID
// is shown to the user with instructions to share via Slack/email. Other
// operators approve via `apigw approvals approve <id>`. When approval
// count reaches the threshold, Apply() is invoked to commit the change.
//
// Storage: bbolt (already a dep) at <StateDir>/approvals.db.
package approvals

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ChangeRequest is the persisted record. Payload carries the mutation
// (action verb + before/after snapshots) the original Submit() supplied.
type ChangeRequest struct {
	ID          string         `json:"id"`
	SubmittedBy string         `json:"submitted_by"`
	SubmittedAt time.Time      `json:"submitted_at"`
	Action      string         `json:"action"`
	Resource    string         `json:"resource"`
	Payload     map[string]any `json:"payload"`
	Threshold   int            `json:"threshold"`
	Approvals   []Approval     `json:"approvals,omitempty"`
	Status      string         `json:"status"` // pending | approved | rejected | applied | cancelled
	AppliedAt   *time.Time     `json:"applied_at,omitempty"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

// Approval records one approver's stamp. Threshold is the # of these
// required before Apply() can run.
type Approval struct {
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

// Store wraps the bbolt-backed change-request log.
type Store struct {
	db *bolt.DB
	mu sync.Mutex
}

const (
	bucketName = "change_requests"
	dbFileName = "approvals.db"
)

// Open initializes the store. Bucket is created lazily.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, dbFileName)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the bbolt handle.
func (s *Store) Close() error { return s.db.Close() }

// Submit creates a new pending ChangeRequest. The returned ID is what
// approvers reference.
//
// `threshold` is the number of approvals required. The submitter does NOT
// count toward their own request.
func (s *Store) Submit(submitter, action, resource string, payload map[string]any, threshold int, ttl time.Duration) (ChangeRequest, error) {
	if threshold < 1 {
		threshold = 1
	}
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	cr := ChangeRequest{
		ID:          newID(),
		SubmittedBy: submitter,
		SubmittedAt: time.Now().UTC(),
		Action:      action,
		Resource:    resource,
		Payload:     payload,
		Threshold:   threshold,
		Status:      "pending",
		ExpiresAt:   time.Now().UTC().Add(ttl),
	}
	return cr, s.save(cr)
}

// Approve adds an approval. Returns the updated record. If the request
// already has Threshold approvals, status flips to "approved" and the
// caller should invoke Apply() to commit.
func (s *Store) Approve(id, approver, comment string) (ChangeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cr, err := s.load(id)
	if err != nil {
		return ChangeRequest{}, err
	}
	if cr.Status != "pending" {
		return cr, fmt.Errorf("approvals: change %s is %s, not pending", id, cr.Status)
	}
	if cr.SubmittedBy == approver {
		return cr, errors.New("approvals: submitter cannot self-approve")
	}
	for _, a := range cr.Approvals {
		if a.By == approver {
			return cr, fmt.Errorf("approvals: %s already approved", approver)
		}
	}
	cr.Approvals = append(cr.Approvals, Approval{By: approver, At: time.Now().UTC(), Comment: comment})
	if len(cr.Approvals) >= cr.Threshold {
		cr.Status = "approved"
	}
	return cr, s.save(cr)
}

// Reject closes the request as rejected. No further approvals accepted.
func (s *Store) Reject(id, by, reason string) (ChangeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, err := s.load(id)
	if err != nil {
		return ChangeRequest{}, err
	}
	cr.Status = "rejected"
	cr.Approvals = append(cr.Approvals, Approval{By: by, At: time.Now().UTC(), Comment: "REJECTED: " + reason})
	return cr, s.save(cr)
}

// MarkApplied is called by the caller after the underlying mutation
// commits. Status flips to "applied" and the request becomes immutable.
func (s *Store) MarkApplied(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cr, err := s.load(id)
	if err != nil {
		return err
	}
	if cr.Status != "approved" {
		return fmt.Errorf("approvals: change %s not approved (status=%s)", id, cr.Status)
	}
	now := time.Now().UTC()
	cr.AppliedAt = &now
	cr.Status = "applied"
	return s.save(cr)
}

// Get returns one request by ID.
func (s *Store) Get(id string) (ChangeRequest, error) {
	return s.load(id)
}

// List returns up to `limit` requests; empty status = all.
func (s *Store) List(status string, limit int) ([]ChangeRequest, error) {
	out := []ChangeRequest{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		c := b.Cursor()
		n := 0
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var cr ChangeRequest
			if err := json.Unmarshal(v, &cr); err != nil {
				continue
			}
			if status != "" && cr.Status != status {
				continue
			}
			out = append(out, cr)
			n++
			if limit > 0 && n >= limit {
				return nil
			}
		}
		return nil
	})
	return out, err
}

// ---------- internals ----------

func (s *Store) save(cr ChangeRequest) error {
	body, _ := json.Marshal(cr)
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketName)).Put([]byte(cr.ID), body)
	})
}

func (s *Store) load(id string) (ChangeRequest, error) {
	var cr ChangeRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketName)).Get([]byte(id))
		if v == nil {
			return fmt.Errorf("approvals: %s not found", id)
		}
		return json.Unmarshal(v, &cr)
	})
	return cr, err
}

// newID returns a short (12-char) collision-resistant ID. 56 bits of
// entropy ≈ 2^28 IDs before 50% birthday collision. Plenty for change
// requests. Encoded as 8-byte big-endian uint64 in hex-ish.
func newID() string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(time.Now().UnixNano()))
	const charset = "abcdefghjkmnpqrstuvwxyz23456789"
	out := make([]byte, 12)
	for i := range out {
		out[i] = charset[int(b[i%8])%len(charset)]
	}
	return string(out)
}
