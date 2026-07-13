package provision

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Driver knows how to bring one kind of datastore into existence and how to
// create a database inside it. Adding MongoDB means adding a Driver; no caller
// changes.
type Driver interface {
	Type() string
	DefaultPort() int

	// Docker describes the container that provides this datastore.
	Docker(s Server) Container

	// Brew is the Homebrew formula providing it locally, or "" if there is none.
	Brew() string

	// Ready reports whether the server is accepting connections *and* answering
	// its own protocol. A port that merely accepts TCP is not the same as a
	// Postgres that has finished initialising.
	Ready(s Server) bool

	// ListDatabases returns the databases that exist. An empty slice with no
	// error means "none"; an error means "could not ask".
	ListDatabases(s Server) ([]string, error)

	// CreateDatabase creates one.
	CreateDatabase(s Server, name string) error
}

// Container is how to run a datastore under Docker.
type Container struct {
	Name  string
	Image string
	Env   []string
	// Ports maps host port to container port.
	Ports map[int]int
	// Volume is a named docker volume mounted at MountPath, so data survives a
	// container being removed and recreated.
	Volume    string
	MountPath string
	Args      []string
}

var drivers = map[string]Driver{
	"postgres":   Postgres{},
	"mysql":      MySQL{},
	"redis":      Redis{},
	"clickhouse": ClickHouse{},
	"mongo":      Mongo{},
	"kafka":      Kafka{},
}

// DriverFor returns the driver for an infra type.
func DriverFor(typ string) (Driver, bool) {
	d, ok := drivers[typ]
	return d, ok
}

// ---------------------------------------------------------------- helpers

// dialable reports whether anything accepts TCP at the address.
func dialable(s Server) bool {
	conn, err := net.DialTimeout("tcp", s.Addr(), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Listening reports whether *something* holds the port, regardless of whether
// wharf can speak to it.
//
// This is the difference between "not running" and "running, but wharf cannot
// authenticate" — and confusing them is expensive: wharf would try to start a
// container on a port that is already taken, fail with a docker networking
// error, and leave the user staring at a message about endpoint binding when
// the real problem was a password.
func Listening(s Server) bool { return dialable(s) }

// run executes a command and returns its trimmed output.
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s: %s", err, text)
		}
		return "", err
	}
	return text, nil
}

// containerName is the docker container wharf uses for a server. It encodes the
// port, so two Postgres servers on different ports do not collide.
func containerName(typ string, port int) string {
	return "wharf-" + typ + "-" + strconv.Itoa(port)
}

// volumeName is the docker volume holding that server's data.
func volumeName(typ string, port int) string {
	return "wharf-" + typ + "-" + strconv.Itoa(port) + "-data"
}

// psqlEnv builds the environment psql needs to connect without prompting.
func pgEnv(s Server) []string {
	env := []string{}
	if s.Password != "" {
		env = append(env, "PGPASSWORD="+s.Password)
	}
	return env
}

// runEnv executes a command with extra environment variables.
func runEnv(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s: %s", err, text)
		}
		return "", err
	}
	return text, nil
}
