// Package provision makes a service's declared infrastructure real.
//
// A service's config already says exactly what it wants — a Postgres at
// localhost:5432 holding a database called billing. wharf's job is to make that
// true, not to invent its own connection details and rewrite the config. So the
// DSN is the specification, and provisioning conforms to it.
//
// Servers are grouped by (type, host, port) rather than by service, because that
// is how they actually exist: one Postgres on 5432 holds a database for each of
// six services. Provisioning one container per service would start six Postgres
// servers to hold six databases, and none of them on the port the configs name.
package provision

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Server is one datastore process, and the databases the workspace needs inside
// it.
type Server struct {
	Type string
	Host string
	Port int

	// User and Password are taken from the DSNs pointing at this server.
	User     string
	Password string

	// Databases are the names services expect to find, mapped to the service
	// that wants each. A service is recorded so a missing database can be
	// reported against the thing that will fail without it.
	Databases map[string]string

	// Conflicts records DSNs that disagree about credentials for this same
	// server. Left for the user to resolve: silently picking one would make a
	// service fail to authenticate for reasons nothing explains.
	Conflicts []string
}

// Addr is host:port.
func (s Server) Addr() string {
	return s.Host + ":" + strconv.Itoa(s.Port)
}

// Key identifies a server uniquely.
func (s Server) Key() string {
	return s.Type + "@" + s.Addr()
}

// SortedDatabases returns the database names in a stable order.
func (s Server) SortedDatabases() []string {
	out := make([]string, 0, len(s.Databases))
	for name := range s.Databases {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PlanFor groups needs into servers, taking credentials from every service in
// the workspace but only the databases wanted by the selected ones.
//
// The split matters. Services disagree about the password for a shared server —
// one DSN says postgres@, another postgres:secret@ — so planning from a subset
// can drop the only credentials that actually work, and wharf then decides the
// server is "down" when it is merely refusing an empty password.
func PlanFor(all, selected []manifest.Service) ([]Server, error) {
	full, err := Plan(all)
	if err != nil {
		return nil, err
	}
	if len(selected) == len(all) {
		return full, nil
	}

	wanted, err := Plan(selected)
	if err != nil {
		return nil, err
	}
	byKey := map[string]Server{}
	for _, s := range wanted {
		byKey[s.Key()] = s
	}

	var out []Server
	for _, s := range full {
		w, ok := byKey[s.Key()]
		if !ok {
			continue // no selected service needs this server
		}
		// Full credentials, narrowed databases.
		s.Databases = w.Databases
		out = append(out, s)
	}
	return out, nil
}

// Plan groups every service's needs into the servers that must exist.
func Plan(services []manifest.Service) ([]Server, error) {
	byKey := map[string]*Server{}

	for _, svc := range services {
		if svc.Kind != manifest.KindService || svc.Disabled {
			continue
		}
		for _, need := range svc.Needs {
			srv, db, user, pass, err := parse(need)
			if err != nil {
				continue // an unparseable DSN is reported by doctor, not here
			}

			key := srv.Key()
			existing, ok := byKey[key]
			if !ok {
				srv.Databases = map[string]string{}
				srv.User, srv.Password = user, pass
				byKey[key] = &srv
				existing = &srv
			}

			// Credentials that disagree for the same server are a real problem:
			// whichever wharf picks, some service authenticates with the other.
			if user != "" && existing.User != "" && user != existing.User {
				existing.Conflicts = append(existing.Conflicts,
					fmt.Sprintf("%s wants user %q, but %q is already in use", svc.Name, user, existing.User))
			}
			if existing.Password == "" && pass != "" {
				existing.Password = pass
			}

			if db != "" {
				existing.Databases[db] = svc.Name
			}
		}
	}

	out := make([]Server, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// parse pulls a server and a database name out of a need's DSN.
func parse(need manifest.Need) (Server, string, string, string, error) {
	srv := Server{Type: need.Type}

	if need.DSN == "" {
		d, ok := drivers[need.Type]
		if !ok {
			return srv, "", "", "", fmt.Errorf("unknown infra type %q", need.Type)
		}
		srv.Host, srv.Port = "localhost", d.DefaultPort()
		return srv, "", "", "", nil
	}

	u, err := url.Parse(need.DSN)
	if err != nil {
		return srv, "", "", "", err
	}

	srv.Host = u.Hostname()
	if srv.Host == "" {
		srv.Host = "localhost"
	}
	// 127.0.0.1 and localhost are the same server; treating them as two would
	// provision the same datastore twice on the same port.
	if srv.Host == "127.0.0.1" {
		srv.Host = "localhost"
	}

	srv.Port = 0
	if p := u.Port(); p != "" {
		srv.Port, _ = strconv.Atoi(p)
	}
	if srv.Port == 0 {
		if d, ok := drivers[need.Type]; ok {
			srv.Port = d.DefaultPort()
		}
	}

	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}

	db := strings.TrimPrefix(u.Path, "/")
	// Redis uses the path as a numeric database index, not a name — there is
	// nothing to create.
	if need.Type == "redis" {
		db = ""
	}
	return srv, db, user, pass, nil
}
