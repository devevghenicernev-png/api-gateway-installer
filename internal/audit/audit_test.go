package audit

import (
	"bytes"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestLogAndQuery(t *testing.T) {
	l := openTemp(t)
	for _, action := range []string{"api.add", "deploy.apply", "secret.rotate"} {
		if _, err := l.Log(Entry{Actor: "alice", Action: action, Resource: "billing", Result: "ok"}); err != nil {
			t.Fatalf("Log %s: %v", action, err)
		}
	}
	count := 0
	_ = l.Query(Filter{}, func(e Entry) bool { count++; return true })
	if count != 3 {
		t.Errorf("expected 3 entries; got %d", count)
	}
}

func TestQueryFilter(t *testing.T) {
	l := openTemp(t)
	_, _ = l.Log(Entry{Actor: "alice", Action: "api.add", Result: "ok"})
	_, _ = l.Log(Entry{Actor: "bob", Action: "deploy.apply", Result: "failed"})
	_, _ = l.Log(Entry{Actor: "alice", Action: "deploy.apply", Result: "ok"})

	got := 0
	_ = l.Query(Filter{Actor: "alice"}, func(e Entry) bool { got++; return true })
	if got != 2 {
		t.Errorf("alice filter: expected 2; got %d", got)
	}
	got = 0
	_ = l.Query(Filter{Result: "failed"}, func(e Entry) bool { got++; return true })
	if got != 1 {
		t.Errorf("failed filter: expected 1; got %d", got)
	}
}

func TestHashChainIntegrity(t *testing.T) {
	l := openTemp(t)
	for i := 0; i < 5; i++ {
		_, err := l.Log(Entry{Actor: "alice", Action: "api.add", Result: "ok"})
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
	}
	broken, err := l.VerifyChain()
	if err != nil || broken != 0 {
		t.Fatalf("chain verify: broken=%d err=%v", broken, err)
	}
}

func TestExportJSONL(t *testing.T) {
	l := openTemp(t)
	for i := 0; i < 3; i++ {
		_, _ = l.Log(Entry{Actor: "alice", Action: "api.add", Resource: "x", Result: "ok"})
	}
	var buf bytes.Buffer
	n, err := l.ExportJSONL(Filter{}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 lines; got %d", n)
	}
	lines := strings.Count(buf.String(), "\n")
	if lines != 3 {
		t.Errorf("expected 3 newlines; got %d", lines)
	}
	if !strings.Contains(buf.String(), `"action":"api.add"`) {
		t.Errorf("missing action in JSONL output")
	}
}

func TestReopenPreservesChainTip(t *testing.T) {
	dir := t.TempDir()
	l1, _ := Open(dir)
	_, _ = l1.Log(Entry{Actor: "alice", Action: "x", Result: "ok"})
	_, _ = l1.Log(Entry{Actor: "alice", Action: "y", Result: "ok"})
	last := l1.lastHash
	id := l1.nextID
	_ = l1.Close()

	l2, _ := Open(dir)
	defer l2.Close()
	if l2.lastHash != last {
		t.Errorf("lastHash drift on reopen: %s != %s", l2.lastHash, last)
	}
	if l2.nextID != id {
		t.Errorf("nextID drift on reopen: %d != %d", l2.nextID, id)
	}
}
