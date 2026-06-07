package auth

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *SessionStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenSessionStore(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSession_IssueAndLookup(t *testing.T) {
	s := newStore(t)
	sess, err := s.Issue("alice", []string{"read", "write"}, map[string]string{"plan": "pro"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if sess.ID == "" {
		t.Fatalf("issue: empty ID")
	}
	if sess.Subject != "alice" {
		t.Fatalf("subject: %s", sess.Subject)
	}
	got, err := s.Lookup(sess.ID, 0)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Subject != "alice" {
		t.Fatalf("lookup subject: %s", got.Subject)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes len: %d", len(got.Scopes))
	}
	if got.Meta["plan"] != "pro" {
		t.Fatalf("meta lost")
	}
}

func TestSession_LookupMissing(t *testing.T) {
	s := newStore(t)
	_, err := s.Lookup("nope", 0)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
	// Empty ID is also not-found, not a server error.
	if _, err := s.Lookup("", 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("empty id: want ErrSessionNotFound, got %v", err)
	}
}

func TestSession_LookupExpired(t *testing.T) {
	s := newStore(t)
	sess, err := s.Issue("bob", nil, nil, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	_, err = s.Lookup(sess.ID, 0)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("want ErrSessionExpired, got %v", err)
	}
}

func TestSession_SlidingWindow(t *testing.T) {
	s := newStore(t)
	sess, _ := s.Issue("eve", nil, nil, 100*time.Millisecond)
	originalExpiry := sess.ExpiresAt
	time.Sleep(50 * time.Millisecond)
	// Lookup with sliding extends expiry.
	got, err := s.Lookup(sess.ID, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !got.ExpiresAt.After(originalExpiry) {
		t.Fatalf("sliding window didn't extend expiry: original=%v new=%v",
			originalExpiry, got.ExpiresAt)
	}
	// LastSeen also updated.
	if !got.LastSeen.After(sess.IssuedAt) {
		t.Fatalf("LastSeen not updated")
	}
}

func TestSession_LastSeenAlwaysUpdated(t *testing.T) {
	s := newStore(t)
	sess, _ := s.Issue("alice", nil, nil, 1*time.Hour)
	originalLastSeen := sess.LastSeen
	time.Sleep(5 * time.Millisecond)
	// Even with slidingTTL=0, LastSeen advances.
	got, err := s.Lookup(sess.ID, 0)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !got.LastSeen.After(originalLastSeen) {
		t.Fatalf("LastSeen not updated under slidingTTL=0")
	}
	// But ExpiresAt unchanged.
	if !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("ExpiresAt drifted without sliding")
	}
}

func TestSession_Revoke(t *testing.T) {
	s := newStore(t)
	sess, _ := s.Issue("carol", nil, nil, 1*time.Hour)
	if err := s.Revoke(sess.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Lookup(sess.ID, 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("post-revoke lookup: %v", err)
	}
	// Idempotent — second revoke is a no-op.
	if err := s.Revoke(sess.ID); err != nil {
		t.Fatalf("revoke (2nd): %v", err)
	}
}

func TestSession_RevokeBySubject(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 5; i++ {
		_, _ = s.Issue("multi-user", nil, nil, 1*time.Hour)
	}
	_, _ = s.Issue("other-user", nil, nil, 1*time.Hour)

	n, err := s.RevokeBySubject("multi-user")
	if err != nil {
		t.Fatalf("revoke-by-subject: %v", err)
	}
	if n != 5 {
		t.Fatalf("deleted %d, want 5", n)
	}
	// The other-user session survives.
	rows, _ := s.List("other-user", 10)
	if len(rows) != 1 {
		t.Fatalf("other-user lost: %d remaining", len(rows))
	}
}

func TestSession_List(t *testing.T) {
	s := newStore(t)
	for _, sub := range []string{"alice", "alice", "bob", "carol"} {
		_, _ = s.Issue(sub, nil, nil, 1*time.Hour)
	}
	all, _ := s.List("", 100)
	if len(all) != 4 {
		t.Fatalf("list all: got %d, want 4", len(all))
	}
	aliceOnly, _ := s.List("alice", 100)
	if len(aliceOnly) != 2 {
		t.Fatalf("alice: got %d, want 2", len(aliceOnly))
	}
	// Limit honoured.
	cap, _ := s.List("", 2)
	if len(cap) != 2 {
		t.Fatalf("limit=2 got %d", len(cap))
	}
}

func TestSession_Cleanup(t *testing.T) {
	s := newStore(t)
	// 3 long-lived + 5 short-lived.
	for i := 0; i < 3; i++ {
		_, _ = s.Issue("long", nil, nil, 1*time.Hour)
	}
	for i := 0; i < 5; i++ {
		_, _ = s.Issue("short", nil, nil, 1*time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	n, err := s.Cleanup()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 5 {
		t.Fatalf("dropped %d, want 5", n)
	}
	if s.Size() != 3 {
		t.Fatalf("size after cleanup: %d, want 3", s.Size())
	}
}

func TestSession_IDUniqueness(t *testing.T) {
	s := newStore(t)
	seen := map[string]struct{}{}
	for i := 0; i < 200; i++ {
		sess, err := s.Issue("uniq", nil, nil, 1*time.Hour)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		if _, dup := seen[sess.ID]; dup {
			t.Fatalf("duplicate session ID at iteration %d: %s", i, sess.ID)
		}
		seen[sess.ID] = struct{}{}
	}
}

func TestSession_IssueValidation(t *testing.T) {
	s := newStore(t)
	if _, err := s.Issue("", nil, nil, 1*time.Hour); err == nil {
		t.Fatalf("empty subject: want error")
	}
	if _, err := s.Issue("alice", nil, nil, 0); err == nil {
		t.Fatalf("ttl=0: want error")
	}
	if _, err := s.Issue("alice", nil, nil, -1*time.Hour); err == nil {
		t.Fatalf("ttl<0: want error")
	}
}

func TestSession_NilSafe(t *testing.T) {
	var s *SessionStore
	if _, err := s.Issue("alice", nil, nil, 1*time.Hour); err == nil {
		t.Fatalf("nil Issue: want error")
	}
	if _, err := s.Lookup("x", 0); err == nil {
		t.Fatalf("nil Lookup: want error")
	}
	if err := s.Revoke("x"); err != nil {
		t.Fatalf("nil Revoke: %v", err)
	}
	if n, err := s.RevokeBySubject("alice"); n != 0 || err != nil {
		t.Fatalf("nil RevokeBySubject: n=%d err=%v", n, err)
	}
	if rows, err := s.List("", 10); rows != nil || err != nil {
		t.Fatalf("nil List: rows=%v err=%v", rows, err)
	}
	if n, err := s.Cleanup(); n != 0 || err != nil {
		t.Fatalf("nil Cleanup: n=%d err=%v", n, err)
	}
	if s.Size() != 0 {
		t.Fatalf("nil Size != 0")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestSession_ConcurrentLookups(t *testing.T) {
	s := newStore(t)
	sess, _ := s.Issue("concurrent", nil, nil, 1*time.Hour)
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := s.Lookup(sess.ID, 1*time.Hour); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent lookup: %v", err)
	}
}

func TestSession_ExpiredMethod(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		s    Session
		want bool
	}{
		{"fresh", Session{ExpiresAt: now.Add(1 * time.Hour)}, false},
		{"just-expired", Session{ExpiresAt: now.Add(-1 * time.Second)}, true},
		{"long-past", Session{ExpiresAt: now.Add(-1 * time.Hour)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.s.Expired() != c.want {
				t.Errorf("Expired() = %v, want %v", c.s.Expired(), c.want)
			}
		})
	}
}
