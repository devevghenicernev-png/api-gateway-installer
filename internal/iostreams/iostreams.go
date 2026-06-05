// Package iostreams wires stdin/stdout/stderr with TTY and color detection.
//
// The contract: every command takes an *IOStreams from the Factory and writes
// only through it. Tests pass a System with bytes.Buffer streams. Production
// passes the real os.Stdin/Stdout/Stderr.
//
// TTY-adaptive output is the design system contract — colored/boxed when
// attached to a terminal, plain when piped. NO_COLOR and APIGW_FORCE_TTY
// honored per the CLI design spec.
package iostreams

import (
	"bytes"
	"io"
	"os"
	"strconv"

	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

// IOStreams is the central IO + TTY/color context.
//
// Fields are exported so commands can write directly (e.g. fmt.Fprintln(ios.Out, ...)),
// but TTY/color queries must go through the methods below — they account for
// piped output and forcing flags.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	stdoutTTY bool
	stderrTTY bool
	stdinTTY  bool

	colorEnabled bool
	is256        bool
	hasTrueColor bool

	// forceTTY: set by APIGW_FORCE_TTY=1 (mirror of GH_FORCE_TTY). Forces
	// stdoutTTY/stderrTTY true even when piped. Useful for asciinema/CI demos.
	forceTTY bool

	// quiet/verbose come from global flags wired post-init via SetQuiet/SetVerbose.
	quiet   bool
	verbose int // 0=none, 1=-v, 2=-vv, 3=-vvv
}

// System returns an IOStreams bound to the real process streams.
func System() *IOStreams {
	stdoutTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	stderrTTY := isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
	stdinTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	force := envBool("APIGW_FORCE_TTY")
	if force {
		stdoutTTY = true
		stderrTTY = true
	}

	color := !envSet("NO_COLOR") && (stdoutTTY || force)
	if envBool("APIGW_NO_COLOR") {
		color = false
	}

	profile := termenv.ColorProfile()
	is256 := color && profile != termenv.Ascii && profile != termenv.ANSI
	trueColor := color && profile == termenv.TrueColor

	return &IOStreams{
		In:           os.Stdin,
		Out:          os.Stdout,
		ErrOut:       os.Stderr,
		stdoutTTY:    stdoutTTY,
		stderrTTY:    stderrTTY,
		stdinTTY:     stdinTTY,
		colorEnabled: color,
		is256:        is256,
		hasTrueColor: trueColor,
		forceTTY:     force,
	}
}

// Test returns an IOStreams suitable for table tests.
//
// All three streams are *bytes.Buffer; the buffers are returned alongside so
// the caller can assert on output. Defaults: NOT a TTY, color disabled.
func Test() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{
		In:     in,
		Out:    out,
		ErrOut: errOut,
	}, in, out, errOut
}

func (io *IOStreams) IsStdoutTTY() bool { return io.stdoutTTY }
func (io *IOStreams) IsStderrTTY() bool { return io.stderrTTY }
func (io *IOStreams) IsStdinTTY() bool  { return io.stdinTTY }

// ColorEnabled reports whether ANSI color escapes should be emitted.
//
// False when piped (unless APIGW_FORCE_TTY=1), when NO_COLOR or APIGW_NO_COLOR
// is set, or when termenv reports a profile that can't render color.
func (io *IOStreams) ColorEnabled() bool { return io.colorEnabled }

func (io *IOStreams) Is256ColorSupported() bool { return io.is256 }
func (io *IOStreams) HasTrueColor() bool        { return io.hasTrueColor }

func (io *IOStreams) SetQuiet(q bool)    { io.quiet = q }
func (io *IOStreams) Quiet() bool        { return io.quiet }
func (io *IOStreams) SetVerbose(v int)   { io.verbose = v }
func (io *IOStreams) Verbose() int       { return io.verbose }
func (io *IOStreams) SetForceTTY(v bool) { io.forceTTY = v }

// SetTTYForTest overrides TTY detection — for use by tests only.
func (io *IOStreams) SetTTYForTest(stdout, stderr, stdin bool) {
	io.stdoutTTY = stdout
	io.stderrTTY = stderr
	io.stdinTTY = stdin
}

// SetColorForTest overrides color detection — for use by tests only.
func (io *IOStreams) SetColorForTest(enabled bool) {
	io.colorEnabled = enabled
}

func envSet(k string) bool { _, ok := os.LookupEnv(k); return ok }

func envBool(k string) bool {
	v, ok := os.LookupEnv(k)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		// Treat any non-empty truthy-looking value as enabled.
		return v == "1" || v == "yes" || v == "on"
	}
	return b
}
