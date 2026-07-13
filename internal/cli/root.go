// Package cli wires the wharf commands together. Each command lives in its own
// file and does nothing but translate flags into calls on the internal
// packages, so the behaviour is testable without a terminal.
package cli

import (
	"fmt"

	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// home is the --home persistent flag: where wharf keeps its state.
var home string

// Execute runs the root command.
func Execute() error {
	root := &cobra.Command{
		Use:   "wharf",
		Short: "Run your local services without remembering how",
		Long: "wharf discovers how each project in your workspace runs, assigns it a\n" +
			"stable port (a berth), and starts it with its dependencies and config.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}

	root.PersistentFlags().StringVar(&home, "home", "", "wharf state directory (default ~/.wharf)")

	root.AddCommand(
		newScanCmd(),
		newListCmd(),
		newTUICmd(),
		newUpCmd(),
		newDBCmd(),
		newBerthCmd(),
		newBootstrapCmd(),
		newDoctorCmd(),
	)
	return root.Execute()
}

// store opens the manifest store using the resolved --home.
func store() (*manifest.Store, error) {
	s, err := manifest.NewStore(home)
	if err != nil {
		return nil, fmt.Errorf("open wharf home: %w", err)
	}
	return s, nil
}
