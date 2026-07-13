package provision

import (
	"fmt"
	"time"
)

// Mode is how a datastore is provided.
type Mode string

const (
	// ModeDocker runs it in a container wharf manages.
	ModeDocker Mode = "docker"
	// ModeLocal expects it on the host — Homebrew, an installer, whatever.
	ModeLocal Mode = "local"
)

// DockerAvailable reports whether the daemon is reachable.
func DockerAvailable() bool {
	_, err := run("docker", "info", "--format", "{{.ServerVersion}}")
	return err == nil
}

// StartDocker brings a server up in a container, creating it if necessary.
//
// The container is named and volumed deterministically, so running this twice
// resumes the same datastore rather than quietly starting a second one with an
// empty disk — which would look exactly like every database vanishing.
func StartDocker(d Driver, s Server) error {
	c := d.Docker(s)

	switch containerState(c.Name) {
	case "running":
		return nil
	case "exited", "created", "paused":
		if _, err := run("docker", "start", c.Name); err != nil {
			return fmt.Errorf("start container %s: %w", c.Name, err)
		}
		return nil
	}

	args := []string{"run", "-d", "--name", c.Name, "--restart", "unless-stopped"}
	for host, container := range c.Ports {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", host, container))
	}
	for _, e := range c.Env {
		args = append(args, "-e", e)
	}
	if c.Volume != "" && c.MountPath != "" {
		args = append(args, "-v", c.Volume+":"+c.MountPath)
	}
	// A label so `wharf infra` can find what it created, and a human can tell
	// these containers apart from their own.
	args = append(args, "--label", "managed-by=wharf")
	args = append(args, c.Image)
	args = append(args, c.Args...)

	if _, err := run("docker", args...); err != nil {
		return fmt.Errorf("run %s: %w", c.Image, err)
	}
	return nil
}

// StopDocker stops the container backing a server, leaving its data volume
// intact — stopping is not deleting.
func StopDocker(d Driver, s Server) error {
	c := d.Docker(s)
	if containerState(c.Name) != "running" {
		return nil
	}
	_, err := run("docker", "stop", c.Name)
	return err
}

// containerState returns docker's state string, or "" when there is no such
// container.
func containerState(name string) string {
	out, err := run("docker", "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return out
}

// WaitReady blocks until the datastore answers its own protocol, not merely
// until its port opens. Postgres accepts TCP for several seconds while it is
// still initialising, and a migration fired in that window fails for reasons
// nothing in the output explains.
func WaitReady(d Driver, s Server, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.Ready(s) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s at %s did not become ready within %s", d.Type(), s.Addr(), timeout)
}

// StartLocal starts a Homebrew-managed service.
func StartLocal(d Driver) error {
	formula := d.Brew()
	if formula == "" {
		return fmt.Errorf("%s has no Homebrew formula; install it yourself or use docker", d.Type())
	}
	if _, err := run("brew", "services", "start", formula); err != nil {
		return fmt.Errorf("brew services start %s: %w", formula, err)
	}
	return nil
}

// LocalInstalled reports whether the Homebrew formula is present.
func LocalInstalled(d Driver) bool {
	if d.Brew() == "" {
		return false
	}
	_, err := run("brew", "list", "--formula", d.Brew())
	return err == nil
}

// EnsureDatabases creates any database a service expects but the server does not
// have. Returns the names it created.
func EnsureDatabases(d Driver, s Server) (created []string, err error) {
	want := s.SortedDatabases()
	if len(want) == 0 {
		return nil, nil
	}

	have, err := d.ListDatabases(s)
	if err != nil {
		return nil, fmt.Errorf("list databases on %s: %w", s.Addr(), err)
	}
	existing := map[string]bool{}
	for _, name := range have {
		existing[name] = true
	}

	for _, name := range want {
		if existing[name] {
			continue
		}
		if err := d.CreateDatabase(s, name); err != nil {
			return created, fmt.Errorf("create database %q: %w", name, err)
		}
		created = append(created, name)
	}
	return created, nil
}

// MissingDatabases reports which expected databases do not exist yet.
func MissingDatabases(d Driver, s Server) ([]string, error) {
	want := s.SortedDatabases()
	if len(want) == 0 {
		return nil, nil
	}
	have, err := d.ListDatabases(s)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	for _, name := range have {
		existing[name] = true
	}

	var missing []string
	for _, name := range want {
		if !existing[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// ContainerFor returns the container name wharf would use, for reporting.
func ContainerFor(typ string, port int) string {
	return containerName(typ, port)
}
