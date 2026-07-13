package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ebnsina/wharf/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:     "tui",
		Aliases: []string{"dash", "ui"},
		Short:   "Open the dashboard",
		Long: "Every service, its berth and its health in one list; the selected service's\n" +
			"live log beside it. Start and stop with a keypress instead of remembering a\n" +
			"command.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI()
		},
	}
}

func runTUI() error {
	st, err := store()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return fmt.Errorf("no services known; run `wharf scan <dir>` first")
	}

	model := tui.New(st, services)

	// AltScreen keeps the dashboard from scrolling the user's scrollback away
	// and restores the terminal on exit. Cell-motion mouse mode gives click and
	// wheel events, which is the difference between a dashboard you point at and
	// one you can only drive from memory.
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}
