// Package ui centralises terminal styling so the CLI and the TUI cannot drift
// apart visually, and so colour can be disabled in one place.
package ui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	Green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	Red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	Yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	Blue   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	Dim    = lipgloss.NewStyle().Faint(true)
	Bold   = lipgloss.NewStyle().Bold(true)
)

// Ok prints a success line.
func Ok(format string, a ...any) {
	fmt.Printf("%s %s\n", Green.Render("✓"), fmt.Sprintf(format, a...))
}

// Fail prints a failure line to stderr.
func Fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Red.Render("✗"), fmt.Sprintf(format, a...))
}

// Warn prints a caution line.
func Warn(format string, a ...any) {
	fmt.Printf("%s %s\n", Yellow.Render("!"), fmt.Sprintf(format, a...))
}

// Info prints a neutral line.
func Info(format string, a ...any) {
	fmt.Printf("%s %s\n", Blue.Render("→"), fmt.Sprintf(format, a...))
}

// Note prints de-emphasised secondary text.
func Note(format string, a ...any) {
	fmt.Println(Dim.Render(fmt.Sprintf(format, a...)))
}
