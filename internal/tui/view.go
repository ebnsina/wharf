package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette is deliberately small. Colour carries meaning here — status,
// severity, emphasis — so every extra hue is one more thing the eye has to
// decode. Adaptive pairs keep it legible on light and dark terminals alike.
var (
	cAccent = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	cGreen  = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cAmber  = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cRed    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cBlue   = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}
	cText   = lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#E5E7EB"}
	cMuted  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	cBorder = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"}
	cSelBg  = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#312E56"}
)

var (
	sBrand = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sMuted = lipgloss.NewStyle().Foreground(cMuted)
	sText  = lipgloss.NewStyle().Foreground(cText)

	sPaneTitle = lipgloss.NewStyle().Bold(true).Foreground(cMuted).Padding(0, 1)

	sPane = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBorder)

	sPaneFocus = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cAccent)

	sKey  = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sHint = lipgloss.NewStyle().Foreground(cMuted)
	sErr  = lipgloss.NewStyle().Foreground(cRed)
)

// statusStyle maps a status to its glyph, colour and label. Shape differs as
// well as colour, so the display still reads for anyone who cannot tell the
// hues apart.
func statusStyle(s Status) (glyph string, color lipgloss.TerminalColor, label string) {
	switch s {
	case StatusHealthy:
		return "●", cGreen, "up"
	case StatusStarting:
		return "◐", cAmber, "starting"
	case StatusFailed:
		return "✗", cRed, "failed"
	case StatusForeign:
		return "◆", cBlue, "external"
	default:
		return "○", cMuted, "stopped"
	}
}

func (m *Model) View() string {
	if !m.ready {
		return "starting wharf…"
	}
	if m.tooSmall() {
		return sErr.Render(fmt.Sprintf(
			"terminal is %d×%d — wharf needs at least %d×%d",
			m.width, m.height, minWidth, minHeight,
		)) + sMuted.Render("\n\npress q to quit")
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		lipgloss.JoinHorizontal(lipgloss.Top, m.renderList(), m.renderLogs()),
		m.renderFooter(),
	)
}

// renderHeader is a single line: what this is, and the state of the fleet at a
// glance. Counts belong here rather than in the list, where they would compete
// with the rows for attention.
func (m *Model) renderHeader() string {
	var up, starting, failed int
	for _, e := range m.entries {
		switch e.status {
		case StatusHealthy, StatusForeign:
			up++
		case StatusStarting:
			starting++
		case StatusFailed:
			failed++
		}
	}

	parts := []string{sMuted.Render(fmt.Sprintf("%d services", len(m.entries)))}
	if up > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(cGreen).Render(fmt.Sprintf("%d up", up)))
	}
	if starting > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(cAmber).Render(fmt.Sprintf("%d starting", starting)))
	}
	if failed > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(cRed).Render(fmt.Sprintf("%d failed", failed)))
	}

	left := sBrand.Render("⚓ wharf")
	right := strings.Join(parts, sMuted.Render(" · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// listRows is how many service rows fit inside the list pane.
func (m *Model) listRows() int {
	// Header line, footer line, pane border (2), pane title (1).
	n := m.height - 5
	if n < 1 {
		n = 1
	}
	return n
}

// window returns the slice of entries to draw, scrolled to keep the cursor
// visible. Without this a workspace of thirty services simply overflows the
// pane and tears the border.
func (m *Model) window() (start, end int) {
	rows := m.listRows()
	if len(m.entries) <= rows {
		return 0, len(m.entries)
	}

	start = m.cursor - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > len(m.entries) {
		start = len(m.entries) - rows
	}
	return start, start + rows
}

func (m *Model) renderList() string {
	inner := listWidth - 2 // inside the border
	start, end := m.window()

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(m.renderRow(i, inner))
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	title := sPaneTitle.Render("SERVICES")
	if start > 0 || end < len(m.entries) {
		title += sMuted.Render(fmt.Sprintf(" %d–%d of %d", start+1, end, len(m.entries)))
	}

	body := lipgloss.JoinVertical(lipgloss.Left, title, b.String())

	pane := sPane
	if !m.logFocus {
		pane = sPaneFocus
	}
	return pane.Width(inner).Height(m.height - 4).Render(body)
}

// renderRow draws one service. Every cell is sized with lipgloss, which measures
// display width rather than bytes — padding with %-*s counts the three bytes of
// "…" as three columns and pushes the row past the pane, which breaks the border
// on exactly the rows whose names were truncated.
func (m *Model) renderRow(i, inner int) string {
	e := m.entries[i]
	selected := i == m.cursor

	glyph, color, _ := statusStyle(e.status)

	marker := " "
	if selected {
		marker = "▸"
	}

	port := ""
	if e.svc.Berth > 0 {
		port = fmt.Sprintf("%d", e.svc.Berth)
	}

	// Fixed columns: marker(1) + space + glyph(1) + space + name + port.
	portW := 6
	nameW := inner - 4 - portW - 1
	if nameW < 6 {
		nameW = 6
	}

	nameStyle := sText
	portStyle := sMuted
	if selected {
		nameStyle = nameStyle.Bold(true)
	}
	if e.status == StatusStopped {
		nameStyle = nameStyle.Foreground(cMuted)
	}

	row := lipgloss.JoinHorizontal(lipgloss.Left,
		lipgloss.NewStyle().Foreground(cAccent).Render(marker),
		" ",
		lipgloss.NewStyle().Foreground(color).Render(glyph),
		" ",
		nameStyle.Width(nameW).Render(truncate(e.svc.Name, nameW)),
		portStyle.Width(portW).Align(lipgloss.Right).Render(port),
	)

	line := lipgloss.NewStyle().Width(inner)
	if selected {
		line = line.Background(cSelBg)
	}
	return line.Render(row)
}

func (m *Model) renderLogs() string {
	width := m.width - listWidth - 2
	if width < minPaneWidth {
		width = minPaneWidth
	}

	title := sPaneTitle.Render("LOGS")
	if e := m.current(); e != nil {
		_, color, label := statusStyle(e.status)
		title = sPaneTitle.Render(strings.ToUpper(e.svc.Name)) +
			lipgloss.NewStyle().Foreground(color).Render(label)

		if e.status == StatusForeign {
			title += sMuted.Render("  berth held by a process wharf did not start")
		}
		if !m.following {
			title += sMuted.Render("  ⏸ scrolled — g to follow")
		}
	}

	body := lipgloss.JoinVertical(lipgloss.Left, title, m.viewport.View())

	pane := sPane
	if m.logFocus {
		pane = sPaneFocus
	}
	return pane.Width(width - 2).Height(m.height - 4).Render(body)
}

func (m *Model) renderFooter() string {
	keys := []struct{ k, d string }{
		{"↑↓", "move"},
		{"s", "start"},
		{"x", "stop"},
		{"r", "restart"},
		{"tab", "pane"},
		{"g", "follow"},
		{"q", "quit"},
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, sKey.Render(k.k)+sHint.Render(" "+k.d))
	}
	line := " " + strings.Join(parts, sMuted.Render("  ·  "))

	if m.err != nil {
		return " " + sErr.Render("✗ "+m.err.Error())
	}
	return line
}

// colorize gives a log line a little structure without parsing it: severity is
// the one thing a developer scans for, and a wall of uniform green hides it.
func colorize(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "ERROR"), strings.Contains(upper, "FATAL"), strings.Contains(upper, "PANIC"):
		return lipgloss.NewStyle().Foreground(cRed).Render(line)
	case strings.Contains(upper, "WARN"):
		return lipgloss.NewStyle().Foreground(cAmber).Render(line)
	case strings.HasPrefix(line, "wharf:"):
		return lipgloss.NewStyle().Foreground(cAccent).Render(line)
	default:
		return sText.Render(line)
	}
}

// truncate shortens to n display columns, measured in runes rather than bytes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
