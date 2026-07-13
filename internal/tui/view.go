package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// statusOf describes how a service presents: a glyph, a colour and a word.
// Shape differs as well as hue, so the list reads without colour too.
func statusOf(s Status) (glyph string, c lipgloss.TerminalColor, label string) {
	switch s {
	case StatusHealthy:
		return "●", green, "running"
	case StatusStarting:
		return "◐", amber, "starting"
	case StatusFailed:
		return "✗", red, "failed"
	case StatusForeign:
		// Occupied, but not by us. Worth its own status: calling it "stopped"
		// would send you hunting for a bug in the service rather than for the
		// process already sitting on its port.
		return "◆", blue, "external"
	default:
		return "○", fgFaint, "stopped"
	}
}

func (m *Model) View() string {
	if !m.ready {
		return stMuted.Render("starting wharf…")
	}
	if m.tooSmall() {
		return stRed.Render(fmt.Sprintf("terminal is %d×%d — wharf needs %d×%d",
			m.width, m.height, minWidth, minHeight)) +
			stFaint.Render("\n\npress q to quit")
	}
	if m.showHelp {
		return m.renderHelp()
	}

	bodyHeight := m.height - 4 // header(2) + footer(2)

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderList(bodyHeight),
		m.renderDivider(bodyHeight),
		m.renderDetail(bodyHeight),
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		body,
		m.renderFooter(),
	)
}

// ------------------------------------------------------------------ chrome

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

	stats := []string{stFaint.Render(fmt.Sprintf("%d services", len(m.entries)))}
	add := func(n int, c lipgloss.TerminalColor, word string) {
		if n > 0 {
			stats = append(stats, lipgloss.NewStyle().Foreground(c).
				Render(fmt.Sprintf("● %d %s", n, word)))
		}
	}
	add(running, green, "running")
	add(starting, amber, "starting")
	add(failed, red, "failed")
	add(external, blue, "external")

	left := stTitle.Render("⚓ wharf")
	right := strings.Join(stats, stFaint.Render("   "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 1 {
		gap = 1
	}

	bar := "  " + left + strings.Repeat(" ", gap) + right + "  "
	rule := lipgloss.NewStyle().Foreground(hairline).
		Render(strings.Repeat("─", max(m.width, 1)))

	return bar + "\n" + rule
}

// renderDivider is the single hairline separating the halves. It replaces two
// full borders — four columns of chrome — with one.
func (m *Model) renderDivider(height int) string {
	col := lipgloss.NewStyle().Foreground(hairline).Render("│")
	rows := make([]string, max(height, 1))
	for i := range rows {
		rows[i] = col
	}
	return strings.Join(rows, "\n")
}

func (m *Model) renderFooter() string {
	rule := lipgloss.NewStyle().Foreground(hairline).
		Render(strings.Repeat("─", max(m.width, 1)))

	if m.err != nil {
		return rule + "\n  " + stRed.Render("✗ "+m.err.Error())
	}

	keys := []struct{ k, d string }{
		{"↑↓", "move"},
		{"s", "start"},
		{"x", "stop"},
		{"r", "restart"},
		{"⇥", "pane"},
		{"?", "help"},
		{"q", "quit"},
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, stKey.Render(k.k)+" "+stHint.Render(k.d))
	}
	return rule + "\n  " + strings.Join(parts, stFaint.Render("   "))
}

// ------------------------------------------------------------------ list

func (m *Model) listRows() int {
	n := m.height - 7 // header(2) + footer(2) + label(1) + gutter(2)
	if n < 1 {
		n = 1
	}
	return n
}

// window scrolls the list so the cursor stays visible. Without it a workspace of
// thirty services simply runs off the bottom.
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

func (m *Model) renderList(height int) string {
	inner := listWidth - 2
	start, end := m.window()

	label := stLabel.Render(spaced("SERVICES"))
	if len(m.entries) > m.listRows() {
		label += stFaint.Render(fmt.Sprintf("   %d/%d", m.cursor+1, len(m.entries)))
	}

	rows := []string{"", label, ""}
	for i := start; i < end; i++ {
		rows = append(rows, m.renderRow(i, inner))
	}

	return lipgloss.NewStyle().
		Width(listWidth).
		Height(height).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
}

// renderRow draws one service.
//
// Cells are sized with lipgloss, which measures display width. Padding with
// %-*s counts bytes, and the ellipsis is three bytes but one column — so exactly
// the rows that needed truncating would overflow their pane.
func (m *Model) renderRow(i, inner int) string {
	e := m.entries[i]
	selected := i == m.cursor

	glyph, color, _ := statusOf(e.status)
	if e.status == StatusStarting {
		glyph = m.spin.View()
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

	// The selected row is filled solid — the only such element on screen — so
	// the glyph and port must invert with it rather than keep their own colours,
	// which would be unreadable on the accent background.
	var dot, name, prt lipgloss.Style
	if selected {
		dot = lipgloss.NewStyle().Foreground(onAcc).Background(accent)
		name = lipgloss.NewStyle().Foreground(onAcc).Background(accent).Bold(true)
		prt = lipgloss.NewStyle().Foreground(onAcc).Background(accent)
	} else {
		dot = lipgloss.NewStyle().Foreground(color)
		name = stFg
		prt = stFaint
		if e.status == StatusStopped {
			name = stMuted
		}
	}

	row := lipgloss.JoinHorizontal(lipgloss.Left,
		dot.Width(3).Align(lipgloss.Center).Render(glyph),
		name.Width(nameW).Render(truncate(e.svc.Name, nameW)),
		prt.Width(portW).Align(lipgloss.Right).Render(port),
	)

	if selected {
		return stSelected.Width(inner).Render(row)
	}
	return lipgloss.NewStyle().Width(inner).Render(row)
}

// ------------------------------------------------------------------ detail

func (m *Model) renderDetail(height int) string {
	width := m.width - listWidth - 1
	if width < minPaneWidth {
		width = minPaneWidth
	}
	inner := width - 4

	e := m.current()
	if e == nil {
		return lipgloss.NewStyle().Width(width).Height(height).Render("")
	}

	glyph, color, label := statusOf(e.status)
	if e.status == StatusStarting {
		glyph = m.spin.View()
	}

	head := lipgloss.JoinHorizontal(lipgloss.Left,
		lipgloss.NewStyle().Bold(true).Foreground(fg).Render(e.svc.Name),
		"   ",
		badge(glyph+" "+label, color),
	)

	sections := []string{"", head, "", m.renderFacts(e, inner), ""}

	// A hairline, not a box: the logs are a continuation of this pane, not a
	// separate thing that needs framing.
	sections = append(sections,
		lipgloss.NewStyle().Foreground(hairline).Render(strings.Repeat("─", max(inner, 1))),
		"",
		m.renderLogBody(e, inner),
	)

	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Padding(0, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, sections...))
}

// renderFacts is the answer to "what is this thing" — the facts you would
// otherwise go and look up, which is the whole reason the tool exists.
func (m *Model) renderFacts(e *entry, inner int) string {
	fact := func(k, v string) string {
		if v == "" {
			return ""
		}
		return stFaint.Width(9).Render(k) + stMuted.Render(v)
	}

	var lines []string

	where := string(e.svc.Stack)
	if e.svc.Berth > 0 {
		where += stFaint.Render("  ·  ") + stMuted.Render(fmt.Sprintf("localhost:%d", e.svc.Berth))
	}
	lines = append(lines, fact("stack", where))

	if len(e.svc.Needs) > 0 {
		var types []string
		for _, n := range e.svc.Needs {
			types = append(types, n.Type)
		}
		lines = append(lines, fact("needs", strings.Join(types, ", ")))
	}

	for _, p := range e.svc.Processes {
		if p.Primary {
			lines = append(lines, fact("run", truncate(p.Cmd, max(inner-10, 1))))
			break
		}
	}

	if e.status == StatusForeign {
		lines = append(lines, "", lipgloss.NewStyle().Foreground(blue).
			Render("this berth is held by a process wharf did not start"))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderLogBody shows output, or explains its absence. A blank pane is a dead
// end; a stopped service should say which key starts it.
func (m *Model) renderLogBody(e *entry, inner int) string {
	if len(e.logs) > 0 {
		return m.viewport.View()
	}

	var msg, hint string
	switch e.status {
	case StatusStarting:
		msg = m.spin.View() + " starting"
		hint = "waiting for it to listen on its berth"
	case StatusForeign:
		msg = "running outside wharf"
		hint = "wharf cannot show logs for a process it did not start"
	case StatusFailed:
		msg = "failed"
		hint = "press r to try again"
	default:
		msg = "not running"
		hint = "press " + stKey.Render("s") + stFaint.Render(" to start")
	}

	block := lipgloss.JoinVertical(lipgloss.Center,
		stMuted.Render(msg),
		"",
		stFaint.Render(hint),
	)
	return lipgloss.NewStyle().
		Width(inner).
		Height(max(m.viewport.Height, 1)).
		Align(lipgloss.Center, lipgloss.Center).
		Render(block)
}

// ------------------------------------------------------------------ help

func (m *Model) renderHelp() string {
	rows := [][2]string{
		{"↑ ↓ / k j", "move between services"},
		{"home end", "first / last"},
		{"", ""},
		{"s / enter", "start"},
		{"x", "stop"},
		{"r", "restart"},
		{"", ""},
		{"tab", "switch focus between the list and the logs"},
		{"g", "follow the log tail"},
		{"b / f", "scroll the log half a page"},
		{"", ""},
		{"?", "close this help"},
		{"q", "quit — stops everything wharf started"},
	}

	var b strings.Builder
	b.WriteString(stTitle.Render("⚓ wharf") + "\n\n")
	b.WriteString(stLabel.Render(spaced("KEYS")) + "\n\n")

	for _, r := range rows {
		if r[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + stKey.Width(14).Render(r[0]) + stMuted.Render(r[1]) + "\n")
	}

	b.WriteString("\n" + stLabel.Render(spaced("STATUS")) + "\n\n")
	for _, s := range []Status{StatusHealthy, StatusStarting, StatusFailed, StatusForeign, StatusStopped} {
		glyph, c, label := statusOf(s)
		note := ""
		if s == StatusForeign {
			note = stFaint.Render("  the berth is taken by a process wharf did not start")
		}
		b.WriteString("  " +
			lipgloss.NewStyle().Foreground(c).Width(3).Render(glyph) +
			stMuted.Width(10).Render(label) + note + "\n")
	}

	return lipgloss.NewStyle().Padding(1, 3).Render(b.String())
}

// colorize gives a log line structure without parsing it. Severity is what a
// developer scans for, and a wall of uniform text hides it.
func colorize(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.HasPrefix(line, "wharf:"):
		return lipgloss.NewStyle().Foreground(accent).Render(line)
	case strings.Contains(upper, "ERROR"), strings.Contains(upper, "FATAL"), strings.Contains(upper, "PANIC"):
		return stRed.Render(line)
	case strings.Contains(upper, "WARN"):
		return lipgloss.NewStyle().Foreground(amber).Render(line)
	// Stack frames are the bulk of a Go panic and rarely the thing you want;
	// dimming them makes the message above them findable.
	case strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    /"), strings.HasPrefix(line, "        /"):
		return stFaint.Render(line)
	default:
		return stFg.Render(line)
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
