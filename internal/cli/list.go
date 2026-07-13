package cli

import (
	"fmt"
	"strings"

	"github.com/ebnsina/wharf/internal/berth"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List services, their berths, and what they need",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(all)
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include libraries and disabled services")
	return cmd
}

func runList(all bool) error {
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

	here, inProject := currentService(services)

	for _, svc := range services {
		if !all && (svc.Kind != manifest.KindService || svc.Disabled) {
			continue
		}

		// A berth that is bound means the service is up — or that something
		// else has taken its slot. Either way you want to see it.
		status := ui.Dim.Render("○")
		if svc.Berth > 0 && berth.InUse(svc.Berth) {
			status = ui.Green.Render("●")
		}

		port := "-"
		if svc.Berth > 0 {
			port = fmt.Sprintf(":%d", svc.Berth)
		}

		var needs []string
		for _, n := range svc.Needs {
			needs = append(needs, n.Type)
		}
		detail := strings.Join(needs, ",")
		if svc.Kind == manifest.KindLibrary {
			detail = "library"
		}

		// The project you are standing in is marked, so a long list still
		// answers "where am I" at a glance.
		marker := " "
		name := svc.Name
		if inProject && svc.Name == here.Name {
			marker = ui.Blue.Render("▸")
			name = ui.Bold.Render(svc.Name) + strings.Repeat(" ", max(30-len(svc.Name), 0))
		} else {
			name = fmt.Sprintf("%-30s", svc.Name)
		}

		fmt.Printf("%s%s %s %-7s %-7s %s\n",
			marker, status, name, port, svc.Stack, ui.Dim.Render(detail))
	}
	return nil
}
