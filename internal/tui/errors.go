package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/iostreams"
)

// ErrorSuggestion is a "try one of: …" entry in a rendered error block.
type ErrorSuggestion struct {
	Cmd  string // e.g. "apigw doctor"
	Desc string // e.g. "inspect ports"
}

// RichError carries the data needed to render the canonical error layout:
//
//	× <one-line summary in lowercase>
//	  <one-line cause, indented>
//
//	  try one of:
//	    <command>          <what it does>
//	    <command>          <what it does>
//
//	  docs: https://apigw.dev/errors/<CODE>
//
// Implements `error` so it can be returned from RunE. The renderer detects it
// via errors.As and pretty-prints; otherwise falls back to .Error().
type RichError struct {
	Summary     string             // lowercase, no exclamation
	Cause       string             // why — one line, indented
	Suggestions []ErrorSuggestion  // 0..N "try one of" entries
	DocsCode    string             // e.g. "E_PORT_BUSY" — turned into URL
	Underlying  error              // wrapped error for errors.Is/As
}

func (e *RichError) Error() string {
	if e.Cause != "" {
		return fmt.Sprintf("%s: %s", e.Summary, e.Cause)
	}
	return e.Summary
}

func (e *RichError) Unwrap() error { return e.Underlying }

// NewError is the constructor for the common case.
func NewError(summary, cause string) *RichError {
	return &RichError{Summary: summary, Cause: cause}
}

// WithFix appends a suggestion. Chainable.
func (e *RichError) WithFix(cmd, desc string) *RichError {
	e.Suggestions = append(e.Suggestions, ErrorSuggestion{Cmd: cmd, Desc: desc})
	return e
}

// WithDocs attaches an error code. Chainable.
func (e *RichError) WithDocs(code string) *RichError { e.DocsCode = code; return e }

// Wrap attaches an underlying error for errors.Is/As. Chainable.
func (e *RichError) Wrap(err error) *RichError { e.Underlying = err; return e }

// Render writes the error to w, styled per IOStreams settings.
// Plain-text fallback when color is disabled (piped output / NO_COLOR).
func (e *RichError) Render(w io.Writer, ios *iostreams.IOStreams) {
	if !ios.ColorEnabled() {
		e.renderPlain(w)
		return
	}
	e.renderStyled(w)
}

func (e *RichError) renderPlain(w io.Writer) {
	fmt.Fprintf(w, "x %s\n", e.Summary)
	if e.Cause != "" {
		fmt.Fprintf(w, "  %s\n", e.Cause)
	}
	if len(e.Suggestions) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  try one of:")
		width := maxCmdWidth(e.Suggestions)
		for _, s := range e.Suggestions {
			fmt.Fprintf(w, "    %-*s  %s\n", width, s.Cmd, s.Desc)
		}
	}
	if e.DocsCode != "" {
		fmt.Fprintf(w, "\n  docs: https://apigw.dev/errors/%s\n", e.DocsCode)
	}
}

func (e *RichError) renderStyled(w io.Writer) {
	fmt.Fprintf(w, "%s %s\n",
		Styles.Danger.Render(GlyphCross),
		Styles.Identifier.Render(e.Summary))
	if e.Cause != "" {
		fmt.Fprintf(w, "  %s\n", Styles.Muted.Render(e.Cause))
	}
	if len(e.Suggestions) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "  "+Styles.Muted.Render("try one of:"))
		width := maxCmdWidth(e.Suggestions)
		for _, s := range e.Suggestions {
			fmt.Fprintf(w, "    %s  %s\n",
				Styles.Accent.Render(padRight(s.Cmd, width)),
				Styles.Muted.Render(s.Desc))
		}
	}
	if e.DocsCode != "" {
		fmt.Fprintf(w, "\n  %s %s\n",
			Styles.Muted.Render("docs:"),
			Styles.URL.Render("https://apigw.dev/errors/"+e.DocsCode))
	}
}

func maxCmdWidth(s []ErrorSuggestion) int {
	w := 0
	for _, x := range s {
		if n := len(x.Cmd); n > w {
			w = n
		}
	}
	return w
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
