package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)

	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("238"))

	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Padding(0, 1)
)

// statusGlyph maps a status to a coloured indicator. Colour alone would exclude
// anyone who cannot distinguish it, so each status also has a distinct shape.
func statusGlyph(s Status) string {
	switch s {
	case StatusHealthy:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("●")
	case StatusStarting:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("◐")
	case StatusFailed:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("✗")
	case StatusForeign:
		// Something else holds the berth. Worth distinguishing: "stopped" would
		// send you looking for a bug in the service rather than for the process
		// squatting its port.
		return lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Render("◆")
	default:
		return dimStyle.Render("○")
	}
}

func (m *Model) View() string {
	if !m.ready {
		return "starting wharf…"
	}
	if m.tooSmall() {
		return fmt.Sprintf(
			"terminal is %dx%d — wharf needs at least %dx%d\n\npress q to quit",
			m.width, m.height, minWidth, minHeight,
		)
	}

	list := m.renderList()
	logs := m.renderLogs()

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, logs)

	footer := helpStyle.Render(
		"↑/↓ select · s start · x stop · r restart · g follow · b/f scroll · q quit",
	)
	if m.err != nil {
		footer = errStyle.Render("✗ " + m.err.Error())
	}

	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

func (m *Model) renderList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("services"))
	b.WriteString("\n")

	for i, e := range m.entries {
		berthText := "     "
		if e.svc.Berth > 0 {
			berthText = fmt.Sprintf(":%-4d", e.svc.Berth)
		}

		name := e.svc.Name
		// Reserve room for the glyph, the berth and the padding.
		if max := listWidth - 12; len(name) > max {
			name = name[:max-1] + "…"
		}

		row := fmt.Sprintf("%s %-*s %s", statusGlyph(e.status), listWidth-12, name, dimStyle.Render(berthText))
		if i == m.cursor {
			row = selectedStyle.Render(fmt.Sprintf("%s %-*s %s",
				statusGlyph(e.status), listWidth-12, name, berthText))
		}

		b.WriteString(row)
		b.WriteString("\n")
	}

	return paneStyle.
		Width(listWidth).
		Height(max(m.height-4, minPaneHeight)).
		Render(b.String())
}

func (m *Model) renderLogs() string {
	title := "logs"
	if e := m.current(); e != nil {
		title = "logs: " + e.svc.Name
		if e.status == StatusForeign {
			title += dimStyle.Render("  (berth held by another process)")
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(title),
		m.viewport.View(),
	)

	return paneStyle.
		Width(max(m.width-listWidth-4, minPaneWidth)).
		Height(max(m.height-4, minPaneHeight)).
		Render(content)
}
