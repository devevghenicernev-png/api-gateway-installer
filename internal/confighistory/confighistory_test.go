package confighistory

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T, capacity int) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenWithCapacity(filepath.Join(dir, "config_history.db"), capacity)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSnapshot_AssignsMonotonicIDs(t *testing.T) {
	s := newStore(t, 100)
	for i := 1; i <= 5; i++ {
		id, err := s.Snapshot([]byte("body-"+fmt.Sprint(i)), "alice", "test", time.Now())
		if err != nil {
			t.Fatalf("snap %d: %v", i, err)
		}
		if id != uint64(i) {
			t.Errorf("id %d: got %d", i, id)
		}
	}
}

func TestList_NewestFirst(t *testing.T) {
	s := newStore(t, 100)
	for i := 1; i <= 3; i++ {
		_, _ = s.Snapshot([]byte("b"), "", "", time.Now())
	}
	list, err := s.List(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len: %d", len(list))
	}
	if list[0].ID != 3 || list[1].ID != 2 || list[2].ID != 1 {
		t.Errorf("wrong order: %v", []uint64{list[0].ID, list[1].ID, list[2].ID})
	}
	if list[0].Body != nil {
		t.Errorf("List should omit Body, got %d bytes", len(list[0].Body))
	}
}

func TestList_LimitHonoured(t *testing.T) {
	s := newStore(t, 100)
	for i := 0; i < 5; i++ {
		_, _ = s.Snapshot([]byte("b"), "", "", time.Now())
	}
	list, _ := s.List(2)
	if len(list) != 2 {
		t.Errorf("limit=2 got %d", len(list))
	}
	if list[0].ID != 5 {
		t.Errorf("limit should still be newest-first: got id=%d", list[0].ID)
	}
}

func TestGet_ReturnsBody(t *testing.T) {
	s := newStore(t, 100)
	id, _ := s.Snapshot([]byte("hello world"), "alice", "ad-hoc", time.Now())
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.Body, []byte("hello world")) {
		t.Errorf("body: %q", got.Body)
	}
	if got.Actor != "alice" || got.Reason != "ad-hoc" {
		t.Errorf("metadata lost: %+v", got)
	}
}

func TestGet_MissingReturnsError(t *testing.T) {
	s := newStore(t, 100)
	if _, err := s.Get(9999); err == nil {
		t.Errorf("missing generation should error")
	}
}

func TestSnapshot_RingTrimsToCapacity(t *testing.T) {
	s := newStore(t, 3)
	for i := 1; i <= 10; i++ {
		_, _ = s.Snapshot([]byte("b"+fmt.Sprint(i)), "", "", time.Now())
	}
	if s.Size() != 3 {
		t.Fatalf("size: %d want 3 (capacity)", s.Size())
	}
	// Newest 3 should be IDs 8/9/10.
	list, _ := s.List(0)
	for i, want := range []uint64{10, 9, 8} {
		if list[i].ID != want {
			t.Errorf("position %d: got %d want %d", i, list[i].ID, want)
		}
	}
}

func TestStore_NilSafe(t *testing.T) {
	var s *Store
	if _, err := s.Snapshot([]byte("x"), "", "", time.Now()); err == nil {
		t.Errorf("nil Snapshot should error")
	}
	if list, err := s.List(0); list != nil || err != nil {
		t.Errorf("nil List: %v / %v", list, err)
	}
	if _, err := s.Get(1); err == nil {
		t.Errorf("nil Get should error")
	}
	if s.Size() != 0 {
		t.Errorf("nil Size != 0")
	}
	if err := s.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}
