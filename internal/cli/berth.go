package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ebnsina/wharf/internal/config"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newBerthCmd() *cobra.Command {
	var dryRun, force bool

	cmd := &cobra.Command{
		Use:   "berth [service...]",
		Short: "Write each service's assigned berth into its own config file",
		Long: "Services read a hard-coded config path and accept no port override, so the\n" +
			"berth has to live in their own config. wharf edits only the port key, keeps\n" +
			"comments and formatting intact, and backs the file up first.\n\n" +
			"A config that is committed to git is skipped: a berth is a choice about this\n" +
			"machine, and writing it into a shared file would put it in everyone else's\n" +
			"checkout. Pass --force to write it anyway.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBerth(args, dryRun, force)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the edits without making them")
	cmd.Flags().BoolVar(&force, "force", false, "write even into a git-tracked config")
	return cmd
}

func runBerth(names []string, dryRun, force bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}
	services = selectServices(services, names)
	if len(services) == 0 {
		return fmt.Errorf("no matching services; run `wharf ls` to see what wharf knows")
	}

	bk := config.NewDirBackupper(filepath.Join(st.Root(), "backups"))

	var changes []*config.Change
	var skipped []string
	var tracked []*config.ErrTracked

	for _, svc := range services {
		if svc.Kind != manifest.KindService || svc.Berth == 0 {
			continue
		}
		src, ok := portConfig(svc)
		if !ok {
			// Nothing to write to. This is not a failure: a service may simply
			// have no config file, and doctor already reports that.
			skipped = append(skipped, svc.Name)
			continue
		}

		path := filepath.Join(svc.Path, src.Path)
		change, err := config.SetPort(
			bk, svc.Name, path, config.Format(src.Format),
			src.PortKey, src.PortTemplate, svc.Berth, dryRun, force,
		)
		if err != nil {
			var te *config.ErrTracked
			if errors.As(err, &te) {
				tracked = append(tracked, te)
				continue
			}
			ui.Fail("%s — %v", svc.Name, err)
			continue
		}
		if change != nil {
			changes = append(changes, change)
		}
	}

	// Report tracked configs separately: they are not errors, they are a
	// deliberate refusal, and the user needs to know a berth was not applied.
	for _, te := range tracked {
		ui.Warn("%-30s skipped — %s is committed to git", te.Service, filepath.Base(te.File))
		ui.Note("    its port stays as-is; gitignore the config, or re-run with --force")
	}

	for _, c := range changes {
		rel := c.File
		if r, err := filepath.Rel(filepath.Dir(filepath.Dir(c.File)), c.File); err == nil {
			rel = r
		}
		ui.Ok("%-30s %s: %s → %s  %s",
			c.Service, c.Key, c.From, ui.Bold.Render(c.To), ui.Dim.Render(rel))
	}

	switch {
	case len(changes) == 0:
		ui.Ok("every service already sits on its berth — nothing to write")
	case dryRun:
		ui.Note("\ndry run — %d files would change, none were written", len(changes))
	default:
		ui.Ok("\nwrote %d config files; originals backed up to %s",
			len(changes), filepath.Join(st.Root(), "backups"))
	}

	if len(skipped) > 0 {
		ui.Note("no writable port key: %v", skipped)
	}
	return nil
}

// portConfig returns the config source that declares the listen port. Only one
// config file owns the port; the others (a .env alongside a config.yaml) are
// left alone.
func portConfig(svc manifest.Service) (manifest.ConfigSource, bool) {
	for _, src := range svc.Config {
		if src.PortKey != "" {
			return src, true
		}
	}
	return manifest.ConfigSource{}, false
}

// selectServices filters by name, or returns everything when no names given.
func selectServices(all []manifest.Service, names []string) []manifest.Service {
	if len(names) == 0 {
		return all
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []manifest.Service
	for _, svc := range all {
		if want[svc.Name] {
			out = append(out, svc)
		}
	}
	return out
}
