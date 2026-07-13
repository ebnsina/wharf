package provision

import (
	"fmt"
	"strconv"
	"strings"
)

// Redis provisions Redis. It has no databases to create — its "databases" are
// numbered slots that always exist — so provisioning is only about the server.
type Redis struct{}

func (Redis) Type() string     { return "redis" }
func (Redis) DefaultPort() int { return 6379 }
func (Redis) Brew() string     { return "redis" }

func (Redis) Docker(s Server) Container {
	return Container{
		Name:      containerName("redis", s.Port),
		Image:     "redis:7-alpine",
		Ports:     map[int]int{s.Port: 6379},
		Volume:    volumeName("redis", s.Port),
		MountPath: "/data",
		Args:      []string{"redis-server", "--appendonly", "yes"},
	}
}

func (r Redis) Ready(s Server) bool {
	if !dialable(s) {
		return false
	}
	out, err := run("redis-cli", "-h", s.Host, "-p", strconv.Itoa(s.Port), "PING")
	return err == nil && strings.Contains(strings.ToUpper(out), "PONG")
}

func (Redis) ListDatabases(Server) ([]string, error) { return nil, nil }
func (Redis) CreateDatabase(Server, string) error    { return nil }

// ClickHouse provisions ClickHouse.
type ClickHouse struct{}

func (ClickHouse) Type() string     { return "clickhouse" }
func (ClickHouse) DefaultPort() int { return 9000 }
func (ClickHouse) Brew() string     { return "clickhouse" }

func (ClickHouse) Docker(s Server) Container {
	env := []string{}
	if s.User != "" && s.User != "default" {
		env = append(env, "CLICKHOUSE_USER="+s.User)
	}
	if s.Password != "" {
		env = append(env, "CLICKHOUSE_PASSWORD="+s.Password)
	}
	return Container{
		Name:  containerName("clickhouse", s.Port),
		Image: "clickhouse/clickhouse-server:24-alpine",
		Env:   env,
		// 9000 is the native protocol the Go clients use; 8123 is HTTP, exposed
		// because every ClickHouse GUI and curl example assumes it.
		Ports:     map[int]int{s.Port: 9000, 8123: 8123},
		Volume:    volumeName("clickhouse", s.Port),
		MountPath: "/var/lib/clickhouse",
	}
}

func (c ClickHouse) Ready(s Server) bool {
	if !dialable(s) {
		return false
	}
	_, err := run("clickhouse-client", append(c.args(s), "--query", "SELECT 1")...)
	return err == nil
}

func (c ClickHouse) ListDatabases(s Server) ([]string, error) {
	out, err := run("clickhouse-client", append(c.args(s), "--query", "SHOW DATABASES")...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (c ClickHouse) CreateDatabase(s Server, name string) error {
	stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", quoteBacktick(name))
	_, err := run("clickhouse-client", append(c.args(s), "--query", stmt)...)
	return err
}

func (ClickHouse) args(s Server) []string {
	args := []string{"--host", s.Host, "--port", strconv.Itoa(s.Port)}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.Password != "" {
		args = append(args, "--password", s.Password)
	}
	return args
}

// Mongo provisions MongoDB. Databases are created implicitly on first write, so
// there is nothing to create up front.
type Mongo struct{}

func (Mongo) Type() string     { return "mongo" }
func (Mongo) DefaultPort() int { return 27017 }
func (Mongo) Brew() string     { return "mongodb-community" }

func (Mongo) Docker(s Server) Container {
	env := []string{}
	if s.User != "" {
		env = append(env, "MONGO_INITDB_ROOT_USERNAME="+s.User)
	}
	if s.Password != "" {
		env = append(env, "MONGO_INITDB_ROOT_PASSWORD="+s.Password)
	}
	return Container{
		Name:      containerName("mongo", s.Port),
		Image:     "mongo:7",
		Env:       env,
		Ports:     map[int]int{s.Port: 27017},
		Volume:    volumeName("mongo", s.Port),
		MountPath: "/data/db",
	}
}

func (Mongo) Ready(s Server) bool { return dialable(s) }

// Mongo creates a database the moment something writes to it, so there is
// nothing to pre-create and nothing meaningful to list.
func (Mongo) ListDatabases(Server) ([]string, error) { return nil, nil }
func (Mongo) CreateDatabase(Server, string) error    { return nil }

// Kafka provisions a broker. Redpanda is used rather than Kafka proper: it is a
// single process with no ZooKeeper, which is the difference between a container
// that starts in two seconds and a stack that needs a compose file.
type Kafka struct{}

func (Kafka) Type() string     { return "kafka" }
func (Kafka) DefaultPort() int { return 9092 }
func (Kafka) Brew() string     { return "" }

func (Kafka) Docker(s Server) Container {
	return Container{
		Name:      containerName("kafka", s.Port),
		Image:     "redpandadata/redpanda:latest",
		Ports:     map[int]int{s.Port: 9092},
		Volume:    volumeName("kafka", s.Port),
		MountPath: "/var/lib/redpanda/data",
		Args: []string{
			"redpanda", "start",
			"--overprovisioned", "--smp", "1", "--memory", "1G",
			"--reserve-memory", "0M", "--node-id", "0", "--check=false",
			"--kafka-addr", "PLAINTEXT://0.0.0.0:9092",
			"--advertise-kafka-addr", "PLAINTEXT://localhost:" + strconv.Itoa(s.Port),
		},
	}
}

func (Kafka) Ready(s Server) bool                    { return dialable(s) }
func (Kafka) ListDatabases(Server) ([]string, error) { return nil, nil }
func (Kafka) CreateDatabase(Server, string) error    { return nil }

// quoteBacktick renders a ClickHouse identifier.
func quoteBacktick(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "") + "`"
}
