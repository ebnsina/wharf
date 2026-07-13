package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ebnsina/wharf/internal/berth"
	"github.com/ebnsina/wharf/internal/detect"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newScanCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "scan [dir...]",
		Short: "Discover projects in a directory and write their manifests",
		Long: "Walks each directory, infers how every project runs, and writes a manifest\n" +
			"per service. Re-running scan preserves any berth you have already been\n" +
			"assigned, so generated configs and gateway routes stay valid.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(args, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be written without writing it")
	return cmd
}

func runScan(dirs []string, dryRun bool) error {
	st, err := store()
	if err != nil {
		return err
	}

	ws, err := st.LoadWorkspace()
	if err != nil && err != manifest.ErrNoWorkspace {
		return err
	}
	if err == manifest.ErrNoWorkspace {
		ws = manifest.DefaultWorkspace()
	}

	// No argument means rescan the roots we already know about.
	if len(dirs) == 0 {
		if len(ws.Roots) == 0 {
			return fmt.Errorf("nothing to scan: pass a directory, e.g. `wharf scan ~/Works`")
		}
		dirs = ws.Roots
	}

	// Existing manifests are the source of berth stability: a service that was
	// already assigned a berth keeps it across rescans, even if its config
	// changed, because gateway routes and other services' URLs point at it.
	existing, err := st.LoadServices()
	if err != nil {
		return err
	}
	prior := map[string]manifest.Service{}
	for _, s := range existing {
		prior[s.Name] = s
	}

	var found []manifest.Service
	for _, dir := range dirs {
		abs, err := filepath.Abs(expandHome(dir))
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("cannot scan %s: %w", abs, err)
		}

		services, err := detect.Scan(abs)
		if err != nil {
			return fmt.Errorf("scan %s: %w", abs, err)
		}
		ui.Info("%s — found %d projects", abs, len(services))
		found = append(found, services...)
		ws.Roots = addRoot(ws.Roots, abs)
	}

	// Carry forward hand edits: the manifest is yours to correct, and a rescan
	// that silently reverted your corrections would make the tool untrustworthy.
	for i := range found {
		if p, ok := prior[found[i].Name]; ok {
			found[i] = merge(p, found[i])
		}
	}

	allocs := berth.Allocate(found, ws.BerthRange)
	byName := map[string]berth.Allocation{}
	for _, a := range allocs {
		byName[a.Service] = a
	}
	for i := range found {
		if a, ok := byName[found[i].Name]; ok {
			found[i].Berth = a.Berth
		}
	}

	if dryRun {
		printScanSummary(found, allocs)
		ui.Note("\ndry run — nothing written")
		return nil
	}

	for _, svc := range found {
		if err := st.SaveService(svc); err != nil {
			return err
		}
	}
	if err := st.SaveWorkspace(ws); err != nil {
		return err
	}

	printScanSummary(found, allocs)
	ui.Ok("wrote %d manifests to %s", len(found), st.ServicesDir())
	ui.Note("edit any manifest to correct what detection got wrong, then `wharf up <service>`")
	return nil
}

// merge keeps a prior manifest's human decisions while accepting newly detected
// facts. Anything a person plausibly hand-edited wins; anything derived from
// files on disk is refreshed.
func merge(prior, fresh manifest.Service) manifest.Service {
	out := fresh

	// A berth, once assigned, is load-bearing: other services and the gateway
	// point at it. Never reassign it behind the user's back.
	if prior.Berth != 0 {
		out.Berth = prior.Berth
	}
	// Explicit human choices that detection has no business overriding.
	out.Disabled = prior.Disabled
	out.DependsOn = prior.DependsOn
	out.Route = prior.Route
	if prior.Kind != "" {
		out.Kind = prior.Kind
	}

	// Preserve per-process autostart choices by name.
	autostart := map[string]*bool{}
	for _, p := range prior.Processes {
		autostart[p.Name] = p.Autostart
	}
	for i, p := range out.Processes {
		if a, ok := autostart[p.Name]; ok {
			out.Processes[i].Autostart = a
		}
	}
	return out
}

func printScanSummary(services []manifest.Service, allocs []berth.Allocation) {
	moved := 0
	for _, a := range allocs {
		if a.Moved {
			moved++
		}
	}

	fmt.Println()
	for _, svc := range services {
		kind := string(svc.Kind)
		port := "-"
		if svc.Berth > 0 {
			port = fmt.Sprintf(":%d", svc.Berth)
		}
		needs := ""
		for i, n := range svc.Needs {
			if i > 0 {
				needs += ","
			}
			needs += n.Type
		}
		fmt.Printf("  %-30s %-8s %-8s %-7s %s\n",
			svc.Name, svc.Stack, kind, port, ui.Dim.Render(needs))
	}

	if moved > 0 {
		fmt.Println()
		ui.Warn("%d services were moved off a contested port:", moved)
		for _, a := range allocs {
			if a.Moved {
				fmt.Printf("    %-30s %d → %d  %s\n",
					a.Service, a.Preferred, a.Berth, ui.Dim.Render("("+a.Reason+")"))
			}
		}
	}
}

func addRoot(roots []string, dir string) []string {
	for _, r := range roots {
		if r == dir {
			return roots
		}
	}
	return append(roots, dir)
}

// expandHome resolves a leading ~ so `wharf scan ~/Works` works from any shell.
func expandHome(p string) string {
	if len(p) == 0 || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
