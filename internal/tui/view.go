package tui

import (
	"fmt"

	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette is small on purpose. Colour carries meaning here — status,
// severity, focus — so every extra hue is one more thing the eye must decode.
// Adaptive pairs keep it legible on light and dark terminals alike.
var (
	cAccent = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	cGreen  = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	cAmber  = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	cRed    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cBlue   = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}

	cText  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	cDim   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	cFaint = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}

	cBorder = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#2A2E37"}
	cSelBg  = lipgloss.AdaptiveColor{Light: "#F3F0FF", Dark: "#2A2545"}
)

var (
	sBrand = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sText  = lipgloss.NewStyle().Foreground(cText)
	sDim   = lipgloss.NewStyle().Foreground(cDim)
	sFaint = lipgloss.NewStyle().Foreground(cFaint)
	sErr   = lipgloss.NewStyle().Foreground(cRed)

	sPane = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBorder).
		Padding(0, 1)

	sPaneFocused = sPane.BorderForeground(cAccent)

	sKey = lipgloss.NewStyle().Bold(true).Foreground(cText)
)

// status describes how a service presents: a glyph, a colour, and a word. Shape
// differs as well as hue, so the list still reads for anyone who cannot
// distinguish the colours.
func statusOf(s Status) (glyph string, c lipgloss.TerminalColor, label string) {
	switch s {
	case StatusHealthy:
		return "●", cGreen, "running"
	case StatusStarting:
		return "◐", cAmber, "starting"
	case StatusFailed:
		return "✗", cRed, "failed"
	case StatusForeign:
		// Not started by wharf, but the berth is occupied. Worth its own status:
		// calling it "stopped" would send you hunting for a bug in the service
		// rather than for the process already sitting on its port.
		return "◆", cBlue, "external"
	default:
		return "○", cFaint, "stopped"
	}
}

func (m *Model) View() string {
	if !m.ready {
		return sDim.Render("starting wharf…")
	}
	if m.tooSmall() {
		return sErr.Render(fmt.Sprintf("terminal is %d×%d — wharf needs %d×%d",
			m.width, m.height, minWidth, minHeight)) +
			sDim.Render("\n\npress q to quit")
	}
	if m.showHelp {
		return m.renderHelp()
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		lipgloss.JoinHorizontal(lipgloss.Top, m.renderList(), m.renderDetail()),
		m.renderFooter(),
	)
}

// ---------------------------------------------------------------- header

func (m *Model) renderHeader() string {
	var running, starting, failed, external int
	for _, e := range m.entries {
		switch e.status {
		case StatusHealthy:
			running++
		case StatusStarting:
			starting++
		case StatusFailed:
			failed++
		case StatusForeign:
			external++
		}
	}

	pills := []string{sFaint.Render(fmt.Sprintf("%d services", len(m.entries)))}
	add := func(n int, c lipgloss.TerminalColor, word string) {
		if n > 0 {
			pills = append(pills, lipgloss.NewStyle().Foreground(c).
				Render(fmt.Sprintf("%d %s", n, word)))
		}
	}
	add(running, cGreen, "running")
	add(starting, cAmber, "starting")
	add(failed, cRed, "failed")
	add(external, cBlue, "external")

	left := sBrand.Render("⚓ wharf")
	right := strings.Join(pills, sFaint.Render("  ·  "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return "\n " + left + strings.Repeat(" ", gap) + right + " \n"
}

// ---------------------------------------------------------------- list

// listRows is how many service rows fit in the pane.
func (m *Model) listRows() int {
	n := m.height - 8 // header(3) + footer(2) + border(2) + title(1)
	if n < 1 {
		n = 1
	}
	return n
}

// window scrolls the list so the cursor stays visible. Without it, a workspace
// of thirty services simply runs past the bottom of the pane.
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
	// Width() on a padded style sets the block width *including* its padding, so
	// the room actually left for a row is two columns narrower than the pane.
	// Sizing rows to the pane width instead makes every row wrap.
	paneW := listWidth - 2 // minus the border
	inner := paneW - 2     // minus the horizontal padding
	start, end := m.window()

	head := sFaint.Render("SERVICES")
	if len(m.entries) > m.listRows() {
		head += sFaint.Render(fmt.Sprintf("  %d/%d", m.cursor+1, len(m.entries)))
	}

	rows := []string{head, ""}
	for i := start; i < end; i++ {
		rows = append(rows, m.renderRow(i, inner))
	}

	pane := sPane
	if m.focus == focusList {
		pane = sPaneFocused
	}
	return pane.
		Width(paneW).
		Height(m.height - 6).
		Render(strings.Join(rows, "\n"))
}

// renderRow draws one service.
//
// Every cell is sized with lipgloss, which measures *display* width. Padding
// with %-*s counts bytes, and the ellipsis is three bytes but one column — so
// exactly the rows that needed truncating would overflow and tear the border.
func (m *Model) renderRow(i, inner int) string {
	e := m.entries[i]
	selected := i == m.cursor
	glyph, color, _ := statusOf(e.status)

	// A left bar reads as selection far better than a faint background alone,
	// and survives terminals with a washed-out palette.
	bar := " "
	if selected {
		bar = "▌"
	}

	port := ""
	if e.svc.Berth > 0 {
		port = fmt.Sprintf("%d", e.svc.Berth)
	}

	const portW = 6
	nameW := inner - 3 - portW
	if nameW < 6 {
		nameW = 6
	}

	name := sText
	switch {
	case selected:
		name = name.Bold(true)
	case e.status == StatusStopped:
		name = name.Foreground(cDim)
	}

	row := lipgloss.JoinHorizontal(lipgloss.Left,
		lipgloss.NewStyle().Foreground(cAccent).Render(bar),
		lipgloss.NewStyle().Foreground(color).Width(2).Align(lipgloss.Center).Render(glyph),
		name.Width(nameW).Render(truncate(e.svc.Name, nameW)),
		sFaint.Width(portW).Align(lipgloss.Right).Render(port),
	)

	line := lipgloss.NewStyle().Width(inner)
	if selected {
		line = line.Background(cSelBg)
	}
	return line.Render(row)
}

// ---------------------------------------------------------------- detail

// renderDetail is the right pane: what this service *is*, then its output. The
// metadata answers the questions you would otherwise go and look up — which
// stack, which port, which database, which command.
func (m *Model) renderDetail() string {
	paneW := m.width - listWidth - 2
	if paneW < minPaneWidth {
		paneW = minPaneWidth
	}
	inner := paneW - 2 // horizontal padding

	e := m.current()
	if e == nil {
		return sPane.Width(paneW).Height(m.height - 6).Render("")
	}

	glyph, color, label := statusOf(e.status)

	title := lipgloss.JoinHorizontal(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Foreground(cText).Render(e.svc.Name),
		"  ",
		lipgloss.NewStyle().Foreground(color).Render(glyph+" "+label),
	)

	meta := m.renderMeta(e, inner)

	rule := sFaint.Render(strings.Repeat("─", max(inner, 1)))

	body := m.renderLogBody(e, inner)

	pane := sPane
	if m.focus == focusLogs {
		pane = sPaneFocused
	}
	return pane.
		Width(paneW).
		Height(m.height - 6).
		Render(lipgloss.JoinVertical(lipgloss.Left, title, meta, rule, body))
}

// renderMeta is the one-glance summary of a service.
func (m *Model) renderMeta(e *entry, inner int) string {
	var facts []string

	facts = append(facts, sDim.Render(string(e.svc.Stack)))
	if e.svc.Berth > 0 {
		facts = append(facts, sDim.Render(fmt.Sprintf("localhost:%d", e.svc.Berth)))
	}
	if len(e.svc.Needs) > 0 {
		var types []string
		for _, n := range e.svc.Needs {
			types = append(types, n.Type)
		}
		facts = append(facts, sDim.Render(strings.Join(types, ", ")))
	}

	line1 := strings.Join(facts, sFaint.Render("  ·  "))

	// The run command is the single most-forgotten fact about a service, so it
	// is shown rather than hidden behind a keystroke.
	cmd := ""
	for _, p := range e.svc.Processes {
		if p.Primary {
			cmd = p.Cmd
			break
		}
	}
	line2 := sFaint.Render("$ " + truncate(cmd, max(inner-2, 1)))

	if e.status == StatusForeign {
		line2 = lipgloss.NewStyle().Foreground(cBlue).
			Render(truncate("berth in use by a process wharf did not start", inner))
	}

	return lipgloss.JoinVertical(lipgloss.Left, line1, line2)
}

// renderLogBody shows output, or explains the absence of it. An empty pane is a
// dead end; a stopped service should say how to start it.
func (m *Model) renderLogBody(e *entry, inner int) string {
	if len(e.logs) > 0 {
		return m.viewport.View()
	}

	var msg, hint string
	switch e.status {
	case StatusForeign:
		msg = "running outside wharf"
		hint = "wharf cannot show logs for a process it did not start"
	case StatusFailed:
		msg = "failed"
		hint = "press r to try again"
	default:
		msg = "not running"
		hint = "press s to start"
	}

	block := lipgloss.JoinVertical(lipgloss.Center,
		sDim.Render(msg),
		"",
		sFaint.Render(hint),
	)
	return lipgloss.NewStyle().
		Width(inner).
		Height(max(m.viewport.Height, 1)).
		Align(lipgloss.Center, lipgloss.Center).
		Render(block)
}

// ---------------------------------------------------------------- chrome

func (m *Model) renderFooter() string {
	if m.err != nil {
		return "\n " + sErr.Render("✗ "+m.err.Error())
	}

	keys := []struct{ k, d string }{
		{"↑↓", "move"},
		{"s", "start"},
		{"x", "stop"},
		{"r", "restart"},
		{"tab", "pane"},
		{"?", "help"},
		{"q", "quit"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, sKey.Render(k.k)+sFaint.Render(" "+k.d))
	}
	return "\n " + strings.Join(parts, sFaint.Render("   "))
}

func (m *Model) renderHelp() string {
	rows := [][2]string{
		{"↑ / k", "previous service"},
		{"↓ / j", "next service"},
		{"home / end", "first / last"},
		{"", ""},
		{"s / enter", "start the selected service"},
		{"x", "stop it"},
		{"r", "restart it"},
		{"", ""},
		{"tab", "move focus between the list and the logs"},
		{"g", "follow the log tail"},
		{"b / f", "scroll the log a half page"},
		{"", ""},
		{"?", "close this help"},
		{"q", "quit — stops everything wharf started"},
	}

	var b strings.Builder
	b.WriteString(sBrand.Render("⚓ wharf") + "\n\n")
	for _, r := range rows {
		if r[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(sKey.Width(14).Render(r[0]) + sDim.Render(r[1]) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(sFaint.Render("status  ") +
		lipgloss.NewStyle().Foreground(cGreen).Render("● running  ") +
		lipgloss.NewStyle().Foreground(cAmber).Render("◐ starting  ") +
		lipgloss.NewStyle().Foreground(cRed).Render("✗ failed  ") +
		lipgloss.NewStyle().Foreground(cBlue).Render("◆ external  ") +
		sFaint.Render("○ stopped") + "\n")
	b.WriteString(sFaint.Render("        ◆ external means the berth is taken by a process wharf did not start"))

	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// colorize gives a log line structure without parsing it. Severity is the one
// thing a developer scans for, and a wall of uniform text hides it.
func colorize(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(line, "wharf:"):
		return lipgloss.NewStyle().Foreground(cAccent).Render(line)
	case strings.Contains(upper, "ERROR"), strings.Contains(upper, "FATAL"), strings.Contains(upper, "PANIC"):
		return lipgloss.NewStyle().Foreground(cRed).Render(line)
	case strings.Contains(upper, "WARN"):
		return lipgloss.NewStyle().Foreground(cAmber).Render(line)
	// Stack frames are the bulk of a Go panic and almost never the thing you
	// are looking for; dimming them makes the message above readable.
	case strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    /"):
		return sFaint.Render(line)
	default:
		return sText.Render(line)
	}
}

// truncate shortens to n display columns, counting runes rather than bytes.
func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
