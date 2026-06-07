// Package changewindow decides whether a mutating admin action is
// allowed at a given moment based on the configured freeze windows.
// Pure-function so the dashboard Guard can call it without import
// loops back into config.
package changewindow

import (
	"strings"
	"time"
)

// Window mirrors config.ChangeWindow (kept here to avoid the
// dashboard → config dependency that already exists from being
// extended into this package).
type Window struct {
	Name      string
	Days      []string // lowercase mon/tue/wed/thu/fri/sat/sun
	StartHour int      // 0-23
	EndHour   int      // 0-23 (exclusive end)
	Reason    string
}

var weekdayNames = map[time.Weekday]string{
	time.Sunday:    "sun",
	time.Monday:    "mon",
	time.Tuesday:   "tue",
	time.Wednesday: "wed",
	time.Thursday:  "thu",
	time.Friday:    "fri",
	time.Saturday:  "sat",
}

// Active returns the first window currently in effect, or nil if
// none. When windows is nil/empty the result is nil.
func Active(windows []Window, now time.Time) *Window {
	if len(windows) == 0 {
		return nil
	}
	day := weekdayNames[now.Weekday()]
	hour := now.Hour()
	for i := range windows {
		w := &windows[i]
		if !dayMatches(w.Days, day) {
			continue
		}
		// EndHour exclusive: window [Start, End). Empty/equal hours
		// mean "all day" — sensible default for "freeze all Sunday".
		start, end := w.StartHour, w.EndHour
		if start == 0 && end == 0 {
			return w
		}
		if start < end {
			if hour >= start && hour < end {
				return w
			}
		} else if start > end {
			// Wraps midnight (shouldn't happen given the multi-window
			// guidance, but handle it).
			if hour >= start || hour < end {
				return w
			}
		}
	}
	return nil
}

// IsMutating reports whether a permission string represents a write
// action. The heuristic: anything NOT ending in one of the read
// suffixes is mutating. Cheap, no per-call permission registry.
func IsMutating(permission string) bool {
	switch {
	case strings.HasSuffix(permission, ".list"),
		strings.HasSuffix(permission, ".show"),
		strings.HasSuffix(permission, ".get"),
		strings.HasSuffix(permission, ".read"),
		strings.HasSuffix(permission, ".status"),
		strings.HasSuffix(permission, ".csrf"): // CSRF token issue is read-side
		return false
	}
	return true
}

func dayMatches(days []string, today string) bool {
	if len(days) == 0 {
		return true // empty = every day
	}
	for _, d := range days {
		if strings.EqualFold(d, today) {
			return true
		}
	}
	return false
}
