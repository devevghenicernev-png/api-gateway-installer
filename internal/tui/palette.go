// Package tui contains the visual design system: the color palette, error
// renderer, plan-card, and the huh-backed Prompter implementation.
//
// All styles use lipgloss.AdaptiveColor so the same code renders correctly
// in dark and light terminals. Glyphs are restricted to ✓ × ⠋ → per the
// CLI design spec (no emoji in default output).
package tui

import "github.com/charmbracelet/lipgloss"

// Palette is the brand color set. Light/Dark variants per the design spec.
// Pick one teal that's distinct from gh blue, fly purple, stripe indigo.
var Palette = struct {
	Primary lipgloss.AdaptiveColor
	Accent  lipgloss.AdaptiveColor
	Success lipgloss.AdaptiveColor
	Warn    lipgloss.AdaptiveColor
	Danger  lipgloss.AdaptiveColor
	Muted   lipgloss.AdaptiveColor
	Subtle  lipgloss.AdaptiveColor
}{
	Primary: lipgloss.AdaptiveColor{Light: "#0B7285", Dark: "#22D3EE"},
	Accent:  lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"},
	Success: lipgloss.AdaptiveColor{Light: "#0E7C3A", Dark: "#34D399"},
	Warn:    lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"},
	Danger:  lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"},
	Muted:   lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"},
	Subtle:  lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#374151"},
}

// Styles are precomputed for the common cases. Build once at package init —
// lipgloss.NewStyle() allocates, so don't rebuild per render in hot paths.
var Styles = struct {
	Heading   lipgloss.Style
	Identifier lipgloss.Style
	Success   lipgloss.Style
	Warn      lipgloss.Style
	Danger    lipgloss.Style
	Muted     lipgloss.Style
	Accent    lipgloss.Style
	URL       lipgloss.Style
	Box       lipgloss.Style
}{
	Heading:    lipgloss.NewStyle().Foreground(Palette.Primary).Bold(true),
	Identifier: lipgloss.NewStyle().Bold(true),
	Success:    lipgloss.NewStyle().Foreground(Palette.Success),
	Warn:       lipgloss.NewStyle().Foreground(Palette.Warn),
	Danger:     lipgloss.NewStyle().Foreground(Palette.Danger),
	Muted:      lipgloss.NewStyle().Foreground(Palette.Muted),
	Accent:     lipgloss.NewStyle().Foreground(Palette.Accent),
	URL:        lipgloss.NewStyle().Foreground(Palette.Accent).Underline(true),
	Box: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Palette.Subtle).
		Padding(0, 1),
}

// Glyphs are the only non-ASCII characters allowed in default output.
const (
	GlyphCheck    = "✓"
	GlyphCross    = "×"
	GlyphSpinner  = "⠋"
	GlyphArrow    = "→"
	GlyphBullet   = "•"
	GlyphPipe     = "│"
)
