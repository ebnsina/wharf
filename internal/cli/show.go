package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebnsina/wharf/internal/berth"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/provision"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show [service]",
		Aliases: []string{"info"},
		Short:   "Everything about one service — how to run it, what it needs, where it listens",
		Long: "Defaults to the project you are standing in, because being inside one is an\n" +
			"unambiguous statement about which service you mean.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store()
			if err != nil {
				return err
			}
			services, err := st.LoadServices()
			if err != nil {
				return err
			}
			svc, err := requireService(services, args)
			if err != nil {
				return err
			}
			return showService(services, svc)
		},
	}
}

// showService prints the facts a developer otherwise goes looking for.
func showService(all []manifest.Service, svc manifest.Service) error {
	status := ui.Dim.Render("stopped")
	if svc.Berth > 0 && berth.InUse(svc.Berth) {
		status = ui.Green.Render("running")
	}

	fmt.Println()
	fmt.Printf("  %s  %s\n", ui.Bold.Render(svc.Name), status)
	fmt.Printf("  %s\n", ui.Dim.Render(shortPath(svc.Path)))
	fmt.Println()

	row := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Printf("  %s %s\n", ui.Dim.Render(pad(k, 10)), v)
	}

	row("stack", string(svc.Stack))
	if svc.Berth > 0 {
		url := fmt.Sprintf("http://localhost:%d", svc.Berth)
		if svc.Declared != 0 && svc.Declared != svc.Berth {
			url += ui.Dim.Render(fmt.Sprintf("   (moved off :%d — it was contested)", svc.Declared))
		}
		row("url", url)
	}

	// The run command is the single most-forgotten fact about a service.
	for _, p := range svc.Processes {
		label := "run"
		cmd := p.Cmd
		if !p.Primary {
			label = p.Name
			if !p.ShouldAutostart() {
				// Say so in the value, not the label: styling the label would
				// pad it by the width of the escape codes and break the column.
				cmd += ui.Dim.Render("   (not started by default)")
			}
		}
		row(label, cmd)
	}

	if svc.Lifecycle.Migrate != "" {
		row("migrate", svc.Lifecycle.Migrate)
	}
	if svc.Lifecycle.Seed != "" {
		row("seed", svc.Lifecycle.Seed)
	}
	if svc.Lifecycle.Test != "" {
		row("test", svc.Lifecycle.Test)
	}

	if len(svc.Config) > 0 {
		var cfgs []string
		for _, c := range svc.Config {
			label := c.Path
			if _, err := os.Stat(filepath.Join(svc.Path, c.Path)); err != nil {
				label += ui.Red.Render(" (missing)")
			}
			cfgs = append(cfgs, label)
		}
		row("config", strings.Join(cfgs, ", "))
	}

	// Datastores, with whether they are actually reachable — the thing that
	// otherwise fails several frames deep inside a driver.
	if len(svc.Needs) > 0 {
		fmt.Println()
		servers, err := provision.PlanFor(all, []manifest.Service{svc})
		if err == nil {
			for _, s := range servers {
				d, ok := provision.DriverFor(s.Type)
				if !ok {
					continue
				}
				mark := ui.Red.Render("✗")
				note := "not running"
				if d.Ready(s) {
					mark = ui.Green.Render("●")
					note = "up"
					if missing, err := provision.MissingDatabases(d, s); err == nil && len(missing) > 0 {
						mark = ui.Yellow.Render("!")
						note = "missing database " + strings.Join(missing, ", ")
					}
				}
				fmt.Printf("  %s %s %s %s\n",
					mark, ui.Dim.Render(pad(s.Type, 8)), s.Addr(), ui.Dim.Render(note))
			}
		}
	}

	if svc.Route != nil {
		fmt.Println()
		row("gateway", svc.Route.Prefix)
	}

	fmt.Println()
	ui.Note("  wharf up       start it, with its datastores")
	ui.Note("  wharf db       open its database")
	ui.Note("  wharf logs     follow its output")
	fmt.Println()
	return nil
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

// shortPath renders a path relative to home, which is how a developer thinks of
// it.
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
