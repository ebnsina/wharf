package provision

import (
	"fmt"
	"strconv"
	"strings"
)

// Postgres provisions PostgreSQL and creates databases inside it.
//
// Commands are issued through psql rather than a Go driver on purpose: it keeps
// wharf free of a database dependency for every datastore it might ever
// support, and psql is already installed wherever anyone actually uses Postgres.
type Postgres struct{}

func (Postgres) Type() string     { return "postgres" }
func (Postgres) DefaultPort() int { return 5432 }
func (Postgres) Brew() string     { return "postgresql@17" }

func (Postgres) Docker(s Server) Container {
	user := s.User
	if user == "" {
		user = "postgres"
	}
	pass := s.Password
	if pass == "" {
		// Postgres refuses to initialise without a password or an explicit
		// trust setting. The configs that omit one expect trust auth, so that
		// is what they get.
		pass = ""
	}

	env := []string{"POSTGRES_USER=" + user}
	if pass != "" {
		env = append(env, "POSTGRES_PASSWORD="+pass)
	} else {
		env = append(env, "POSTGRES_HOST_AUTH_METHOD=trust")
	}
	// A default database must exist for the server to finish starting; the ones
	// services actually want are created afterwards.
	env = append(env, "POSTGRES_DB=postgres")

	return Container{
		Name:      containerName("postgres", s.Port),
		Image:     "postgres:17-alpine",
		Env:       env,
		Ports:     map[int]int{s.Port: 5432},
		Volume:    volumeName("postgres", s.Port),
		MountPath: "/var/lib/postgresql/data",
	}
}

// Ready asks Postgres itself, not just the socket: during initialisation the
// port is open for several seconds before the server will answer a query, and a
// migration fired at that moment fails for no visible reason.
func (p Postgres) Ready(s Server) bool {
	if !dialable(s) {
		return false
	}
	_, err := runEnv(pgEnv(s), "psql", append(p.psqlArgs(s, "postgres"), "SELECT 1")...)
	return err == nil
}

func (p Postgres) ListDatabases(s Server) ([]string, error) {
	out, err := runEnv(pgEnv(s), "psql",
		append(p.psqlArgs(s, "postgres"), "SELECT datname FROM pg_database WHERE datistemplate = false")...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (p Postgres) CreateDatabase(s Server, name string) error {
	// The name comes from a config file, so it is quoted rather than
	// interpolated bare: a database called "stream-v2" is not a valid bare
	// identifier and would be a syntax error.
	stmt := fmt.Sprintf("CREATE DATABASE %s", quoteIdent(name))
	_, err := runEnv(pgEnv(s), "psql", append(p.psqlArgs(s, "postgres"), stmt)...)
	return err
}

// psqlArgs are the flags every psql invocation needs.
//
// ON_ERROR_STOP is the important one: without it psql can exit zero after a
// statement has failed, so wharf would report a database as created when the
// CREATE never happened — and the service would then fail to connect to a
// database wharf had just claimed to make.
func (p Postgres) psqlArgs(s Server, db string) []string {
	return []string{"-v", "ON_ERROR_STOP=1", p.dsn(s, db), "-tAc"}
}

// dsn builds a libpq URL for a given database on this server.
func (p Postgres) dsn(s Server, db string) string {
	user := s.User
	if user == "" {
		user = "postgres"
	}
	auth := user
	if s.Password != "" {
		auth += ":" + s.Password
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable",
		auth, s.Host, s.Port, db)
}

// MySQL provisions MySQL.
type MySQL struct{}

func (MySQL) Type() string     { return "mysql" }
func (MySQL) DefaultPort() int { return 3306 }
func (MySQL) Brew() string     { return "mysql" }

func (MySQL) Docker(s Server) Container {
	pass := s.Password
	env := []string{}
	if pass != "" {
		env = append(env, "MYSQL_ROOT_PASSWORD="+pass)
	} else {
		env = append(env, "MYSQL_ALLOW_EMPTY_PASSWORD=yes")
	}
	return Container{
		Name:      containerName("mysql", s.Port),
		Image:     "mysql:8",
		Env:       env,
		Ports:     map[int]int{s.Port: 3306},
		Volume:    volumeName("mysql", s.Port),
		MountPath: "/var/lib/mysql",
	}
}

func (m MySQL) Ready(s Server) bool {
	if !dialable(s) {
		return false
	}
	_, err := run("mysql", append(m.args(s), "-e", "SELECT 1")...)
	return err == nil
}

func (m MySQL) ListDatabases(s Server) ([]string, error) {
	out, err := run("mysql", append(m.args(s), "-N", "-B", "-e", "SHOW DATABASES")...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (m MySQL) CreateDatabase(s Server, name string) error {
	stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", strings.ReplaceAll(name, "`", ""))
	_, err := run("mysql", append(m.args(s), "-e", stmt)...)
	return err
}

func (MySQL) args(s Server) []string {
	user := s.User
	if user == "" {
		user = "root"
	}
	args := []string{"-h", s.Host, "-P", strconv.Itoa(s.Port), "-u", user}
	if s.Password != "" {
		args = append(args, "-p"+s.Password)
	}
	return args
}

// ---------------------------------------------------------------- helpers

// lines splits command output into non-empty trimmed lines.
func lines(out string) []string {
	var result []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			result = append(result, l)
		}
	}
	return result
}

// quoteIdent renders a name as a safe SQL identifier. Names come from config
// files, and "stream-v2" is not a valid bare identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
