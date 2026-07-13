package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebnsina/wharf/internal/berth"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report what will break before you start anything",
		Long: "Checks the things that normally fail at runtime with a confusing error:\n" +
			"contested ports, missing config files, services pointing at a dependency\n" +
			"that nothing serves, and infra that is not running.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor()
		},
	}
}

// problem is one finding, kept as data so the TUI can render the same checks.
type problem struct {
	Service string
	Detail  string
	Fix     string
}

func runDoctor() error {
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

	var problems []problem
	problems = append(problems, checkConflicts(services)...)
	problems = append(problems, checkConfigs(services)...)
	problems = append(problems, checkRunCommands(services)...)

	if len(problems) == 0 {
		ui.Ok("no problems found across %d services", len(services))
		return nil
	}

	for _, p := range problems {
		ui.Fail("%s — %s", ui.Bold.Render(p.Service), p.Detail)
		if p.Fix != "" {
			fmt.Printf("    %s\n", ui.Dim.Render(p.Fix))
		}
	}
	fmt.Println()
	ui.Warn("%d problems found", len(problems))
	return nil
}

// checkConflicts reports ports that more than one project declares. wharf will
// reassign berths to resolve these, but the user should know their projects
// disagree — a hard-coded URL somewhere may still point at the old port.
func checkConflicts(services []manifest.Service) []problem {
	var out []problem
	for _, c := range berth.Conflicts(services) {
		out = append(out, problem{
			Service: strings.Join(c.Services, ", "),
			Detail:  fmt.Sprintf("all declare port %d in their own config", c.Port),
			Fix:     "wharf has assigned distinct berths; run `wharf ls` to see them",
		})
	}
	return out
}

// checkConfigs catches the fresh-clone failure: the template is committed but
// the real config was never created, so the service dies on startup with an
// error that names a file rather than a fix.
func checkConfigs(services []manifest.Service) []problem {
	var out []problem
	for _, svc := range services {
		if svc.Kind != manifest.KindService {
			continue
		}
		for _, src := range svc.Config {
			path := filepath.Join(svc.Path, src.Path)
			if _, err := os.Stat(path); err == nil {
				continue
			}
			p := problem{
				Service: svc.Name,
				Detail:  fmt.Sprintf("config %s does not exist", src.Path),
			}
			if src.Template != "" {
				p.Fix = fmt.Sprintf("cp %s %s", filepath.Join(svc.Path, src.Template), path)
			}
			out = append(out, p)
		}
	}
	return out
}

// checkRunCommands catches a service wharf detected but cannot start, which is
// better said now than at 2am.
func checkRunCommands(services []manifest.Service) []problem {
	var out []problem
	for _, svc := range services {
		if svc.Kind != manifest.KindService || svc.Disabled {
			continue
		}
		if len(svc.Processes) == 0 {
			out = append(out, problem{
				Service: svc.Name,
				Detail:  "no run command detected",
				Fix:     "add a `processes:` entry to its manifest",
			})
			continue
		}
		if svc.Berth == 0 {
			out = append(out, problem{
				Service: svc.Name,
				Detail:  "no port found in its config",
				Fix:     "set `berth:` in its manifest so dependents and the gateway can reach it",
			})
		}
	}
	return out
}
