package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/ebnsina/wharf/internal/infra"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newDBCmd() *cobra.Command {
	var kind string

	cmd := &cobra.Command{
		Use:   "db [service]",
		Short: "Open a database shell for a service",
		Long: "Reads the connection string out of the service's own config and opens the\n" +
			"right client with it — psql, redis-cli, clickhouse-client. You never look up\n" +
			"a host, port, user or database name again.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDB(args, kind)
		},
	}
	cmd.Flags().StringVar(&kind, "type", "", "which datastore, when a service has several (postgres, redis, clickhouse)")
	return cmd
}

func runDB(args []string, kind string) error {
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
	name := svc.Name
	if len(svc.Needs) == 0 {
		return fmt.Errorf("%s has no datastore that wharf could find", name)
	}

	need, err := pickNeed(svc, kind)
	if err != nil {
		return err
	}

	bin, args, err := infra.Shell(need)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%s is not installed — needed to open a %s shell", bin, need.Type)
	}

	// Check first: otherwise the client prints its own connection error, which
	// says less than we already know.
	if statuses := infra.Check([]manifest.Need{need}); len(infra.Down(statuses)) > 0 {
		ui.Warn("%s does not appear to be running at %s", need.Type, statuses[0].Addr)
	}

	ui.Note("%s %s", bin, need.DSN)

	// Hand over the terminal: a database shell is interactive, so wharf must get
	// out of the way entirely rather than pipe it.
	c := exec.Command(bin, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// pickNeed selects which datastore to open when a service has several.
func pickNeed(svc manifest.Service, kind string) (manifest.Need, error) {
	if kind != "" {
		for _, n := range svc.Needs {
			if n.Type == kind {
				return n, nil
			}
		}
		return manifest.Need{}, fmt.Errorf("%s has no %s dependency", svc.Name, kind)
	}

	if len(svc.Needs) == 1 {
		return svc.Needs[0], nil
	}

	// Prefer the primary datastore over a cache: opening redis-cli when someone
	// typed `wharf db api` is almost never what they meant.
	for _, preferred := range []string{"postgres", "mysql", "clickhouse", "mongo"} {
		for _, n := range svc.Needs {
			if n.Type == preferred {
				return n, nil
			}
		}
	}
	return svc.Needs[0], nil
}
