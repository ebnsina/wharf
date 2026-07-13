package tui

import "github.com/charmbracelet/lipgloss"

// The look is built on restraint rather than decoration.
//
// Boxes around everything add four lines of chrome per pane and give the eye a
// border to trace instead of content to read. A single hairline between the two
// halves does the same job with none of the weight. Emphasis then comes from
// type and one accent colour, not from rules — which is why the selected row is
// a solid block: it is the only strongly-filled thing on screen, so it cannot be
// missed even at a glance.
var (
	accent = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	onAcc  = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0F0D17"}

	green = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	amber = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	red   = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	blue  = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}

	// Three weights of text, and no more. Anything finer stops reading as a
	// hierarchy and starts reading as inconsistency.
	fg      = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E8E8EE"}
	fgMuted = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B8FA3"}
	fgFaint = lipgloss.AdaptiveColor{Light: "#B0B4BE", Dark: "#4A4E5C"}

	hairline = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#2A2D38"}
)

var (
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	stFg    = lipgloss.NewStyle().Foreground(fg)
	stMuted = lipgloss.NewStyle().Foreground(fgMuted)
	stFaint = lipgloss.NewStyle().Foreground(fgFaint)

	// stLabel is the small-caps section heading. Letter-spacing is faked with a
	// space between glyphs, which is the only way to get it in a terminal.
	stLabel = lipgloss.NewStyle().Foreground(fgFaint).Bold(true)

	// stSelected is the one filled element on screen.
	stSelected = lipgloss.NewStyle().Background(accent).Foreground(onAcc).Bold(true)

	stKey  = lipgloss.NewStyle().Bold(true).Foreground(fgMuted)
	stHint = lipgloss.NewStyle().Foreground(fgFaint)
	stRed  = lipgloss.NewStyle().Foreground(red)
)

// badge renders a status as a filled pill. Colour and shape both carry the
// meaning, so it still reads without colour.
func badge(text string, c lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().
		Foreground(onAcc).
		Background(c).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

// spaced fakes letter-spacing for section headings.
func spaced(s string) string {
	out := make([]rune, 0, len(s)*2)
	for i, r := range s {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, r)
	}
	return string(out)
}
