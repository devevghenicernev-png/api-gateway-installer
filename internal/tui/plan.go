package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/devevghenicernev-png/apigw/internal/iostreams"
)

// PlanRow is one line in a plan card: "Key   Value".
type PlanRow struct {
	Key   string
	Value string
}

// PlanCard renders the boxed "here's what I'll do" view used before any
// mutating operation per the design system rule "plan, then apply".
type PlanCard struct {
	Title string
	Rows  []PlanRow
}

// NewPlan starts a card with the given title.
func NewPlan(title string) *PlanCard { return &PlanCard{Title: title} }

// Add appends a row. Chainable.
func (p *PlanCard) Add(key, value string) *PlanCard {
	p.Rows = append(p.Rows, PlanRow{Key: key, Value: value})
	return p
}

// Render writes the card to w. Plain (TSV) when piped, boxed when TTY.
func (p *PlanCard) Render(w io.Writer, ios *iostreams.IOStreams) {
	if !ios.ColorEnabled() {
		p.renderPlain(w)
		return
	}
	p.renderBoxed(w)
}

func (p *PlanCard) renderPlain(w io.Writer) {
	fmt.Fprintf(w, "# %s\n", p.Title)
	width := 0
	for _, r := range p.Rows {
		if n := len(r.Key); n > width {
			width = n
		}
	}
	for _, r := range p.Rows {
		fmt.Fprintf(w, "%-*s  %s\n", width, r.Key, r.Value)
	}
}

func (p *PlanCard) renderBoxed(w io.Writer) {
	keyWidth := 0
	for _, r := range p.Rows {
		if n := lipgloss.Width(r.Key); n > keyWidth {
			keyWidth = n
		}
	}
	var b strings.Builder
	for i, r := range p.Rows {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s  %s",
			Styles.Muted.Render(padRight(r.Key, keyWidth)),
			Styles.Identifier.Render(r.Value))
	}
	body := b.String()

	header := Styles.Heading.Render(p.Title)
	card := lipgloss.JoinVertical(lipgloss.Left,
		header,
		Styles.Muted.Render(strings.Repeat("─", lipgloss.Width(header))),
		body,
	)
	fmt.Fprintln(w, Styles.Box.Render(card))
}
