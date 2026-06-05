package tui

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// SpinnerRegistry enforces the design rule "one spinner, never nested" by
// tracking active spinners across the process. The second concurrent
// caller gets a passive (no-spinner) handle that still completes via
// Done() — preserving the call site's structure without visual conflict.
//
// Usage:
//
//	sp := tui.Spin(w, "obtaining certificate")
//	defer sp.Done()
//
// When stdout isn't a TTY, Spin() returns a no-op handle that just prints
// the label once + a final ✓/× — keeping log output clean.
type SpinnerRegistry struct {
	active atomic.Int32
}

var globalSpinner SpinnerRegistry

// Spinner is what callers hold. Done() finalises the line.
type Spinner struct {
	registry *SpinnerRegistry
	stop     chan struct{}
	wg       sync.WaitGroup
	w        io.Writer
	label    string
	passive  bool
	tty      bool
}

// Spin starts a spinner on w with the given label.
func Spin(w io.Writer, label string) *Spinner {
	return SpinTTY(w, label, true)
}

// SpinTTY lets callers force-disable the TTY animation path (used by
// tests + the --json mode wrapper).
func SpinTTY(w io.Writer, label string, tty bool) *Spinner {
	s := &Spinner{
		registry: &globalSpinner,
		stop:     make(chan struct{}),
		w:        w,
		label:    label,
		tty:      tty,
	}
	// Only one active spinner at a time.
	if !globalSpinner.active.CompareAndSwap(0, 1) {
		s.passive = true
	}
	if !s.passive && s.tty {
		s.wg.Add(1)
		go s.animate()
	} else {
		fmt.Fprintf(w, "› %s …\n", label)
	}
	return s
}

func (s *Spinner) animate() {
	defer s.wg.Done()
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			fmt.Fprintf(s.w, "\r\x1b[K%s %s", frames[i], s.label)
			i = (i + 1) % len(frames)
		}
	}
}

// Done finalises the line with a ✓. Idempotent.
func (s *Spinner) Done() {
	s.finish(true, "")
}

// Fail finalises the line with a ×. Idempotent.
func (s *Spinner) Fail(reason string) {
	s.finish(false, reason)
}

func (s *Spinner) finish(ok bool, reason string) {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
		return // already finished
	default:
		close(s.stop)
	}
	if !s.passive && s.tty {
		s.wg.Wait()
	}
	mark := GlyphCheck
	style := Styles.Success
	if !ok {
		mark = GlyphCross
		style = Styles.Danger
	}
	if !s.passive && s.tty {
		fmt.Fprintf(s.w, "\r\x1b[K%s %s", style.Render(mark), s.label)
		if reason != "" {
			fmt.Fprintf(s.w, " — %s", Styles.Muted.Render(reason))
		}
		fmt.Fprintln(s.w)
	} else if reason != "" {
		fmt.Fprintf(s.w, "%s %s — %s\n", style.Render(mark), s.label, reason)
	} else {
		fmt.Fprintf(s.w, "%s %s\n", style.Render(mark), s.label)
	}
	if !s.passive {
		s.registry.active.Store(0)
	}
}
