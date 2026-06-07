package changewindow

import (
	"testing"
	"time"
)

func TestActive_EmptyReturnsNil(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if w := Active(nil, now); w != nil {
		t.Errorf("nil windows: got %+v", w)
	}
	if w := Active([]Window{}, now); w != nil {
		t.Errorf("empty windows: got %+v", w)
	}
}

func TestActive_MatchByHour(t *testing.T) {
	w := []Window{{Name: "biz", StartHour: 9, EndHour: 17}}
	// Wednesday 10:00 → in window
	in := time.Date(2026, 1, 7, 10, 0, 0, 0, time.UTC) // Wed
	if Active(w, in) == nil {
		t.Errorf("expected match at 10:00")
	}
	// Wednesday 17:00 → end-exclusive, NOT in window
	end := time.Date(2026, 1, 7, 17, 0, 0, 0, time.UTC)
	if Active(w, end) != nil {
		t.Errorf("17:00 should be excluded (EndHour is exclusive)")
	}
	// Wednesday 03:00 → out
	out := time.Date(2026, 1, 7, 3, 0, 0, 0, time.UTC)
	if Active(w, out) != nil {
		t.Errorf("3:00 should be out")
	}
}

func TestActive_MatchByDay(t *testing.T) {
	w := []Window{{Name: "fri", Days: []string{"fri"}, StartHour: 14, EndHour: 24}}
	fri := time.Date(2026, 1, 9, 15, 0, 0, 0, time.UTC) // Fri 15:00
	thu := time.Date(2026, 1, 8, 15, 0, 0, 0, time.UTC) // Thu 15:00
	if Active(w, fri) == nil {
		t.Errorf("Friday window: should match Fri 15:00")
	}
	if Active(w, thu) != nil {
		t.Errorf("Friday-only: should NOT match Thu")
	}
}

func TestActive_AllDay(t *testing.T) {
	w := []Window{{Name: "wholeday", Days: []string{"sun"}, StartHour: 0, EndHour: 0}}
	sun := time.Date(2026, 1, 4, 3, 0, 0, 0, time.UTC) // Sun 3am
	if Active(w, sun) == nil {
		t.Errorf("all-day Sunday: 03:00 should match")
	}
}

func TestActive_WrapMidnight(t *testing.T) {
	w := []Window{{Name: "night", StartHour: 22, EndHour: 6}}
	in1 := time.Date(2026, 1, 7, 23, 0, 0, 0, time.UTC) // 23:00 (>=22)
	in2 := time.Date(2026, 1, 7, 2, 0, 0, 0, time.UTC)  // 02:00 (<6)
	out := time.Date(2026, 1, 7, 10, 0, 0, 0, time.UTC) // 10:00 (neither)
	if Active(w, in1) == nil {
		t.Errorf("23:00: should match midnight-wrapping window")
	}
	if Active(w, in2) == nil {
		t.Errorf("02:00: should match midnight-wrapping window")
	}
	if Active(w, out) != nil {
		t.Errorf("10:00: should NOT match midnight-wrapping window")
	}
}

func TestActive_FirstMatchWins(t *testing.T) {
	w := []Window{
		{Name: "first", StartHour: 9, EndHour: 17},
		{Name: "second", StartHour: 12, EndHour: 13},
	}
	noon := time.Date(2026, 1, 7, 12, 30, 0, 0, time.UTC)
	got := Active(w, noon)
	if got == nil || got.Name != "first" {
		t.Errorf("first-match-wins: got %+v", got)
	}
}

func TestIsMutating(t *testing.T) {
	cases := []struct {
		perm string
		want bool
	}{
		{"api.add", true},
		{"api.delete", true},
		{"deploy.run", true},
		{"deploy.rollback", true},
		{"api.list", false},
		{"api.show", false},
		{"api.get", false},
		{"api.read", false},
		{"api.status", false},
		{"csrf.csrf", false},
	}
	for _, c := range cases {
		if got := IsMutating(c.perm); got != c.want {
			t.Errorf("IsMutating(%q) = %v, want %v", c.perm, got, c.want)
		}
	}
}

func TestActive_DayMatchCaseInsensitive(t *testing.T) {
	w := []Window{{Name: "mixed", Days: []string{"MON", "Tue"}, StartHour: 0, EndHour: 24}}
	mon := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC) // Mon
	if Active(w, mon) == nil {
		t.Errorf("uppercase day name should match")
	}
}
