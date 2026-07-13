package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ebnsina/wharf/internal/infra"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newBootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap <service>",
		Short: "Take a freshly cloned service from nothing to running",
		Long: "Creates its config from the committed template, installs dependencies, brings\n" +
			"up its infrastructure, migrates and seeds. These are the steps that normally\n" +
			"live in a README nobody has updated.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrap(args[0])
		},
	}
}

func runBootstrap(name string) error {
	st, err := store()
	if err != nil {
		return err
	}
	svc, err := st.LoadService(name)
	if err != nil {
		return err
	}

	// 1. Config. A fresh clone has the template but not the config, and the
	// service dies on startup naming a file rather than a fix.
	for _, src := range svc.Config {
		path := filepath.Join(svc.Path, src.Path)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if src.Template == "" {
			ui.Warn("%s is missing and has no template to copy", src.Path)
			continue
		}
		tmpl := filepath.Join(svc.Path, src.Template)
		data, err := os.ReadFile(tmpl)
		if err != nil {
			return fmt.Errorf("read template %s: %w", tmpl, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		ui.Ok("created %s from %s", src.Path, src.Template)
	}

	// 2. Dependencies.
	if err := step(svc, "install", svc.Lifecycle.Install); err != nil {
		return err
	}

	// 3. Infrastructure, before migrating into a database that is not there.
	if len(svc.Needs) > 0 {
		down := infra.Down(infra.Check(svc.Needs))
		for _, s := range down {
			if s.Compose == "" {
				ui.Warn("%s at %s is down; wharf has no compose file to start it", s.Need.Type, s.Addr)
				continue
			}
			ui.Info("starting %s via %s", s.Need.Type, s.Compose)
			if err := infra.ComposeUp(svc.Path, s.Compose); err != nil {
				return err
			}
		}
	}

	// 4. Schema, then data.
	if err := step(svc, "migrate", svc.Lifecycle.Migrate); err != nil {
		return err
	}
	if err := step(svc, "seed", svc.Lifecycle.Seed); err != nil {
		return err
	}

	ui.Ok("%s is ready — `wharf up %s`", svc.Name, svc.Name)
	return nil
}

// step runs one lifecycle command, streaming its output so a failing migration
// shows why rather than just failing.
func step(svc manifest.Service, label, cmdline string) error {
	if cmdline == "" {
		return nil
	}
	ui.Info("%s: %s", label, ui.Dim.Render(cmdline))

	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Dir = svc.Path
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", label, err)
	}
	return nil
}
