// Package infra checks the datastores a service depends on.
//
// Almost none of these services provide their own infrastructure — they assume
// Postgres, Redis, ClickHouse and Kafka are already listening on localhost. When
// that assumption is wrong the service dies inside a driver, several frames deep,
// with a message about a refused connection and no mention of which datastore or
// which service. Probing first turns that into one clear line.
package infra

import (
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"github.com/ebnsina/wharf/internal/manifest"
)

// defaultPorts let wharf probe a datastore whose DSN omits the port.
var defaultPorts = map[string]int{
	"postgres":   5432,
	"mysql":      3306,
	"redis":      6379,
	"clickhouse": 9000,
	"mongo":      27017,
	"kafka":      9092,
	"rabbitmq":   5672,
}

// Status is the result of probing one dependency.
type Status struct {
	Need manifest.Need
	Addr string
	Up   bool
	// Compose names a compose file that could provide this, when the service
	// ships one.
	Compose string
}

// Check probes every need and reports what is reachable.
func Check(needs []manifest.Need) []Status {
	out := make([]Status, 0, len(needs))
	for _, n := range needs {
		addr, err := address(n)
		if err != nil {
			// An unparseable DSN is reported as down rather than dropped: the
			// user should see that wharf could not make sense of it.
			out = append(out, Status{Need: n, Addr: "?", Up: false, Compose: n.Compose})
			continue
		}
		out = append(out, Status{
			Need:    n,
			Addr:    addr,
			Up:      reachable(addr),
			Compose: n.Compose,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Need.Type < out[j].Need.Type })
	return out
}

// Down returns only the unreachable dependencies.
func Down(statuses []Status) []Status {
	var out []Status
	for _, s := range statuses {
		if !s.Up {
			out = append(out, s)
		}
	}
	return out
}

// address extracts host:port from a need's DSN, filling in the datastore's
// default port when the DSN omits it.
func address(n manifest.Need) (string, error) {
	if n.DSN == "" {
		port, ok := defaultPorts[n.Type]
		if !ok {
			return "", fmt.Errorf("no DSN and no default port for %s", n.Type)
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}

	u, err := url.Parse(n.DSN)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		p, ok := defaultPorts[n.Type]
		if !ok {
			return "", fmt.Errorf("dsn has no port and %s has no default", n.Type)
		}
		port = strconv.Itoa(p)
	}
	return net.JoinHostPort(host, port), nil
}

// reachable reports whether something accepts TCP connections at addr. This is
// deliberately shallow: wharf is answering "is it listening", not "is it
// healthy", and a real protocol handshake per datastore would buy little.
func reachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ComposeUp starts a compose file's services in the background. It is only
// called when the user asks: silently starting containers on someone's machine
// is not wharf's decision to make.
func ComposeUp(dir, file string) error {
	cmd := exec.Command("docker", "compose", "-f", file, "up", "-d")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose up: %w\n%s", err, out)
	}
	return nil
}

// Shell returns the command that opens an interactive client for a datastore,
// with the connection details already filled in. This is the whole point of
// recording DSNs: you never look one up again.
func Shell(n manifest.Need) (string, []string, error) {
	if n.DSN == "" {
		return "", nil, fmt.Errorf("no connection string known for %s", n.Type)
	}

	switch n.Type {
	case "postgres":
		return "psql", []string{n.DSN}, nil

	case "redis":
		return "redis-cli", []string{"-u", n.DSN}, nil

	case "mysql":
		u, err := url.Parse(n.DSN)
		if err != nil {
			return "", nil, err
		}
		args := []string{"-h", u.Hostname()}
		if p := u.Port(); p != "" {
			args = append(args, "-P", p)
		}
		if u.User != nil {
			args = append(args, "-u", u.User.Username())
			if pw, ok := u.User.Password(); ok {
				args = append(args, "-p"+pw)
			}
		}
		if db := database(u); db != "" {
			args = append(args, db)
		}
		return "mysql", args, nil

	case "clickhouse":
		u, err := url.Parse(n.DSN)
		if err != nil {
			return "", nil, err
		}
		args := []string{"--host", u.Hostname()}
		if p := u.Port(); p != "" {
			args = append(args, "--port", p)
		}
		if u.User != nil {
			args = append(args, "--user", u.User.Username())
			if pw, ok := u.User.Password(); ok && pw != "" {
				args = append(args, "--password", pw)
			}
		}
		if db := database(u); db != "" {
			args = append(args, "--database", db)
		}
		return "clickhouse-client", args, nil

	case "mongo":
		return "mongosh", []string{n.DSN}, nil

	default:
		return "", nil, fmt.Errorf("no shell known for %s", n.Type)
	}
}

// database pulls the database name out of a DSN path.
func database(u *url.URL) string {
	if len(u.Path) > 1 {
		return u.Path[1:]
	}
	return ""
}
