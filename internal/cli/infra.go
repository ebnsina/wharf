package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ebnsina/wharf/internal/provision"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newInfraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "infra",
		Short: "Bring up the datastores your services expect",
		Long: "Your services already say what they need — a Postgres at localhost:5432 with a\n" +
			"database called billing. wharf makes that true rather than inventing its own\n" +
			"connection details and rewriting your config.\n\n" +
			"Servers are grouped by address, because that is how they exist: one Postgres\n" +
			"holds a database for each of several services.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInfraStatus()
		},
	}
	cmd.AddCommand(newInfraUpCmd(), newInfraDownCmd())
	return cmd
}

// loadPlan reads the manifests and groups their needs into servers.
func loadPlan() ([]provision.Server, error) {
	st, err := store()
	if err != nil {
		return nil, err
	}
	services, err := st.LoadServices()
	if err != nil {
		return nil, err
	}
	return provision.Plan(services)
}

func runInfraStatus() error {
	servers, err := loadPlan()
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		ui.Ok("no service declares a datastore")
		return nil
	}

	for _, s := range servers {
		d, ok := provision.DriverFor(s.Type)
		if !ok {
			ui.Warn("%s at %s — wharf does not know how to provide this", s.Type, s.Addr())
			continue
		}

		up := d.Ready(s)
		mark := ui.Red.Render("✗")
		state := "down"
		if up {
			mark = ui.Green.Render("●")
			state = "up"
		}

		fmt.Printf("%s %-11s %-22s %s\n", mark, s.Type, s.Addr(), ui.Dim.Render(state))

		if !up {
			for _, db := range s.SortedDatabases() {
				fmt.Printf("    %s %s\n", ui.Dim.Render("needs"), db)
			}
			continue
		}

		// The server is up, but a database it is supposed to hold may not exist
		// — which fails at migration time with an error naming neither.
		missing, err := provision.MissingDatabases(d, s)
		if err != nil {
			ui.Note("    could not list databases: %v", err)
			continue
		}
		for _, db := range s.SortedDatabases() {
			if contains(missing, db) {
				fmt.Printf("    %s %-24s %s\n",
					ui.Red.Render("✗"), db, ui.Dim.Render("missing — "+s.Databases[db]+" needs it"))
			} else {
				fmt.Printf("    %s %-24s %s\n",
					ui.Green.Render("✓"), db, ui.Dim.Render(s.Databases[db]))
			}
		}
	}

	// Credentials that disagree about the same server are worth saying out loud:
	// whichever wharf uses, some service will fail to authenticate.
	for _, s := range servers {
		for _, c := range s.Conflicts {
			ui.Warn("%s — %s", s.Addr(), c)
		}
	}
	return nil
}

func newInfraUpCmd() *cobra.Command {
	var mode string
	var yes bool

	cmd := &cobra.Command{
		Use:   "up [service...]",
		Short: "Start missing datastores and create the databases services expect",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInfraUp(args, mode, yes)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "how to provide a missing datastore: docker | local")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask; use --mode, defaulting to docker")
	return cmd
}

func runInfraUp(names []string, mode string, yes bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	all, err := st.LoadServices()
	if err != nil {
		return err
	}

	// Narrow to the named services, so `wharf infra up billing` does not start
	// every datastore in the workspace. Standing inside a project narrows it the
	// same way, without having to name it.
	services := all
	if len(names) == 0 {
		if svc, ok := currentService(all); ok {
			names = []string{svc.Name}
			ui.Note("in %s", svc.Name)
		}
	}
	if len(names) > 0 {
		services = selectServices(all, names)
		if len(services) == 0 {
			return fmt.Errorf("no matching services")
		}
	}

	// Credentials are taken from every service, databases only from the selected
	// ones: services disagree about the password for a shared server, and a
	// subset can drop the only one that works.
	servers, err := provision.PlanFor(all, services)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		ui.Ok("nothing to provide")
		return nil
	}

	for _, s := range servers {
		if err := bringUp(s, mode, yes); err != nil {
			ui.Fail("%s at %s — %v", s.Type, s.Addr(), err)
			continue
		}
	}
	return nil
}

// bringUp starts one server if needed, then creates its missing databases.
func bringUp(s provision.Server, mode string, yes bool) error {
	d, ok := provision.DriverFor(s.Type)
	if !ok {
		return fmt.Errorf("wharf does not know how to provide %s", s.Type)
	}

	if !d.Ready(s) && provision.Listening(s) {
		// Something holds the port but will not talk to us. Starting a container
		// here fails with a docker networking error about endpoint binding,
		// which says nothing about the actual problem: the credentials.
		return fmt.Errorf(
			"something is listening at %s but wharf could not authenticate — check the credentials in the service's config",
			s.Addr())
	}

	if !d.Ready(s) {
		chosen, err := chooseMode(s, d, mode, yes)
		if err != nil {
			return err
		}

		switch chosen {
		case provision.ModeDocker:
			ui.Info("starting %s in docker on %s", s.Type, s.Addr())
			if err := provision.StartDocker(d, s); err != nil {
				return err
			}
		case provision.ModeLocal:
			ui.Info("starting %s via homebrew", s.Type)
			if err := provision.StartLocal(d); err != nil {
				return err
			}
		default:
			ui.Note("skipped %s at %s", s.Type, s.Addr())
			return nil
		}

		// Wait for the protocol, not just the port: Postgres accepts TCP while
		// still initialising, and a database created in that window fails.
		ui.Note("waiting for %s to accept connections…", s.Type)
		if err := provision.WaitReady(d, s, 90*time.Second); err != nil {
			return err
		}
		ui.Ok("%s is up at %s", s.Type, s.Addr())
	}

	created, err := provision.EnsureDatabases(d, s)
	if err != nil {
		return err
	}
	for _, db := range created {
		ui.Ok("created database %s %s", ui.Bold.Render(db), ui.Dim.Render("for "+s.Databases[db]))
	}
	if len(created) == 0 && len(s.Databases) > 0 {
		ui.Ok("%s has every database it needs", s.Type)
	}
	return nil
}

// chooseMode asks how a missing datastore should be provided, unless told.
func chooseMode(s provision.Server, d provision.Driver, mode string, yes bool) (provision.Mode, error) {
	if mode != "" {
		switch provision.Mode(mode) {
		case provision.ModeDocker, provision.ModeLocal:
			return provision.Mode(mode), nil
		default:
			return "", fmt.Errorf("unknown mode %q; use docker or local", mode)
		}
	}
	if yes {
		return provision.ModeDocker, nil
	}

	// Nobody is there to answer. Defaulting to docker here would start
	// containers unasked from a script or a CI job, which is not a decision to
	// make on someone's behalf from a closed stdin.
	if !interactive() {
		return "", fmt.Errorf(
			"%s is not running and there is no terminal to ask on; pass --mode docker|local", s.Type)
	}

	docker := provision.DockerAvailable()
	local := provision.LocalInstalled(d)

	fmt.Println()
	ui.Warn("%s is not running at %s", ui.Bold.Render(s.Type), s.Addr())
	for _, db := range s.SortedDatabases() {
		ui.Note("    %s needs the database %s", s.Databases[db], db)
	}
	fmt.Println()

	// Present only what is actually possible. Offering "local" when the formula
	// is not installed just produces a failure two steps later.
	fmt.Printf("  %s docker   %s\n", ui.Bold.Render("[d]"),
		ui.Dim.Render(dockerHint(docker, d, s)))
	fmt.Printf("  %s local    %s\n", ui.Bold.Render("[l]"),
		ui.Dim.Render(localHint(local, d)))
	fmt.Printf("  %s skip\n", ui.Bold.Render("[s]"))
	fmt.Print("\n  choice [d]: ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "d", "docker":
		if !docker {
			return "", fmt.Errorf("docker is not running")
		}
		return provision.ModeDocker, nil
	case "l", "local":
		return provision.ModeLocal, nil
	default:
		return "", nil // skip
	}
}

func dockerHint(available bool, d provision.Driver, s provision.Server) string {
	if !available {
		return "docker is not running"
	}
	return d.Docker(s).Image + " — wharf runs and keeps it"
}

func localHint(installed bool, d provision.Driver) string {
	formula := d.Brew()
	if formula == "" {
		return "no homebrew formula; install it yourself"
	}
	if installed {
		return "brew services start " + formula
	}
	return "not installed — brew install " + formula + " first"
}

func newInfraDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the datastore containers wharf started",
		Long:  "Stops them. Data volumes are left alone — stopping is not deleting.",
		RunE: func(cmd *cobra.Command, args []string) error {
			servers, err := loadPlan()
			if err != nil {
				return err
			}
			for _, s := range servers {
				d, ok := provision.DriverFor(s.Type)
				if !ok {
					continue
				}
				if err := provision.StopDocker(d, s); err != nil {
					ui.Fail("%s at %s — %v", s.Type, s.Addr(), err)
					continue
				}
				ui.Ok("stopped %s", provision.ContainerFor(s.Type, s.Port))
			}
			return nil
		},
	}
}

// interactive reports whether stdin is a terminal a human could answer on.
//
// Checking for a character device is not enough: /dev/null is one, so a command
// run with stdin redirected would "ask", read EOF, and take the default —
// starting containers nobody agreed to.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
