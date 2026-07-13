package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ebnsina/wharf/internal/config"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/orchestrator"
	"github.com/ebnsina/wharf/internal/process"
	"github.com/ebnsina/wharf/internal/provision"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	var (
		withInfra bool
		noBerth   bool
		all       bool
	)

	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Start services, their dependencies, and their infrastructure",
		Long: "Resolves what each service needs, checks its datastores are reachable, puts\n" +
			"it on its berth, and starts it. Runs in the foreground and streams the logs;\n" +
			"Ctrl-C stops everything it started, as a process group.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUp(args, withInfra, noBerth, all)
		},
	}
	cmd.Flags().BoolVar(&withInfra, "infra", false, "provision missing datastores and create their databases")
	cmd.Flags().BoolVar(&noBerth, "no-berth", false, "do not write berths into config files")
	cmd.Flags().BoolVar(&all, "all", false, "start every enabled service")
	return cmd
}

func runUp(names []string, withInfra, noBerth, everything bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	all, err := st.LoadServices()
	if err != nil {
		return err
	}

	// No names inside a project means that project. Standing in one says which
	// service you mean; making you type its name again ignores it.
	if len(names) == 0 && !everything {
		names, err = resolveNames(all, names)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("name a service, run this from inside one, or pass --all")
		}
		ui.Note("in %s", names[0])
	}

	services, err := orchestrator.Resolve(all, names)
	if err != nil {
		return err
	}
	// Libraries have nothing to start.
	services = runnable(services)
	if len(services) == 0 {
		return fmt.Errorf("nothing to start")
	}

	if err := ensureInfra(all, services, withInfra); err != nil {
		return err
	}
	if !noBerth {
		if err := ensureBerths(st, services); err != nil {
			return err
		}
	}

	// Buffered generously: a starting dev server emits a burst of output, and a
	// dropped line is a line the user cannot see.
	events := make(chan process.Event, 512)
	groups := make([]*process.Group, 0, len(services))

	// Stop everything on the way out, whatever the reason. Registered before
	// the first start, so a failure midway still tears down what came before.
	defer func() {
		for _, g := range groups {
			_ = g.Stop(5 * time.Second)
		}
	}()

	go render(events)

	for _, svc := range services {
		g := process.NewGroup(svc.Name, specs(st, svc))
		groups = append(groups, g)

		ui.Info("starting %s", ui.Bold.Render(svc.Name))
		if err := g.Start(events); err != nil {
			return fmt.Errorf("start %s: %w", svc.Name, err)
		}

		if err := awaitHealthy(svc); err != nil {
			return fmt.Errorf("%s never became healthy: %w", svc.Name, err)
		}
		if svc.Berth > 0 {
			ui.Ok("%-28s http://localhost:%d", svc.Name, svc.Berth)
		} else {
			ui.Ok("%s", svc.Name)
		}
	}

	fmt.Println()
	ui.Note("running — Ctrl-C to stop")

	// Block until interrupted. The deferred Stop then signals every process
	// group, which reaches air's compiled binary and pnpm's vite alike.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	fmt.Println()
	ui.Info("stopping…")
	return nil
}

// runnable drops libraries, which are built rather than served.
func runnable(services []manifest.Service) []manifest.Service {
	var out []manifest.Service
	for _, s := range services {
		if s.Kind == manifest.KindService && !s.Disabled {
			out = append(out, s)
		}
	}
	return out
}

// specs turns a manifest's processes into supervisor specs, honouring each
// process's autostart flag so a rarely-needed poller stays off.
func specs(st *manifest.Store, svc manifest.Service) []process.Spec {
	var out []process.Spec
	for _, p := range svc.Processes {
		if !p.ShouldAutostart() {
			continue
		}
		dir := svc.Path
		if p.Dir != "" {
			dir = filepath.Join(svc.Path, p.Dir)
		}
		out = append(out, process.Spec{
			Service: svc.Name,
			Name:    p.Name,
			Cmd:     p.Cmd,
			Dir:     dir,
			Env:     p.Env,
			LogPath: st.LogPath(svc.Name, p.Name),
		})
	}
	return out
}

// awaitHealthy waits for the service to answer on its berth.
func awaitHealthy(svc manifest.Service) error {
	if svc.Health == nil || svc.Berth == 0 {
		return nil
	}
	timeout := time.Duration(svc.Health.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return process.WaitHealthy(ctx, process.Health{
		Type: svc.Health.Type,
		Port: svc.Berth,
		Path: svc.Health.Path,
	})
}

// ensureInfra checks that every datastore a service needs is actually there —
// the server *and* the database inside it.
//
// A service whose Postgres is up but whose database does not exist fails on its
// first query, several frames deep in a driver, with a message that names
// neither the service nor the database. Saying so first is the whole point.
func ensureInfra(all, services []manifest.Service, provisionIt bool) error {
	servers, err := provision.PlanFor(all, services)
	if err != nil {
		return err
	}

	var problems int
	for _, s := range servers {
		d, ok := provision.DriverFor(s.Type)
		if !ok {
			continue
		}

		if !d.Ready(s) {
			if !provisionIt {
				ui.Fail("%s is not running at %s", s.Type, s.Addr())
				problems++
				continue
			}
			if err := bringUp(s, "", false); err != nil {
				return err
			}
			continue
		}

		missing, err := provision.MissingDatabases(d, s)
		if err != nil {
			continue
		}
		if len(missing) == 0 {
			continue
		}

		if !provisionIt {
			for _, db := range missing {
				ui.Fail("%s has no database %q — %s needs it", s.Type, db, s.Databases[db])
			}
			problems++
			continue
		}
		created, err := provision.EnsureDatabases(d, s)
		if err != nil {
			return err
		}
		for _, db := range created {
			ui.Ok("created database %s", ui.Bold.Render(db))
		}
	}

	if problems > 0 {
		return fmt.Errorf("%d datastore problems; run `wharf infra up` or pass --infra", problems)
	}
	return nil
}

// ensureBerths writes each service's berth into its own config before start.
func ensureBerths(st *manifest.Store, services []manifest.Service) error {
	bk := config.NewDirBackupper(filepath.Join(st.Root(), "backups"))

	for _, svc := range services {
		src, ok := portConfig(svc)
		if !ok || svc.Berth == 0 {
			continue
		}
		path := filepath.Join(svc.Path, src.Path)
		change, err := config.SetPort(
			bk, svc.Name, path, config.Format(src.Format),
			src.PortKey, src.PortTemplate, svc.Berth, false, false,
		)
		if err != nil {
			return fmt.Errorf("%s: %w", svc.Name, err)
		}
		if change != nil {
			ui.Note("%s: %s %s → %s", svc.Name, change.Key, change.From, change.To)
		}
	}
	return nil
}

// render prints multiplexed output, prefixed so a line can be traced back to
// the process that produced it.
func render(events <-chan process.Event) {
	for ev := range events {
		switch ev.Kind {
		case process.EventLog:
			fmt.Printf("%s %s\n", ui.Dim.Render(label(ev)), ev.Line)
		case process.EventExit:
			if ev.Err != nil {
				ui.Fail("%s exited: %v", label(ev), ev.Err)
			}
		}
	}
}

func label(ev process.Event) string {
	name := ev.Service
	if ev.Process != "" && ev.Process != "api" && ev.Process != "web" {
		name += "/" + ev.Process
	}
	return fmt.Sprintf("%-22s │", truncate(name, 22))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
