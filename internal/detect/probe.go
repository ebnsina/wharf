package detect

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
	"gopkg.in/yaml.v3"
)

// probeResult is what reading a service's config file tells us.
type probeResult struct {
	// PortKey is the dotted path to the listen port, e.g. "app.port" or
	// "http_addr". Empty when no port could be located.
	PortKey string
	// PortTemplate is how the value is written, with {port} as the placeholder:
	// "{port}", ":{port}", "0.0.0.0:{port}".
	PortTemplate string
	Port         int
	Needs        []manifest.Need
}

// PortPlaceholder is the token a port template substitutes.
const PortPlaceholder = "{port}"

// candidate is one place a config might live.
type candidate struct {
	Path     string
	Template string
	Format   manifest.ConfigFormat
}

// structuredCandidates are the single-config-file conventions, most specific
// first.
var structuredCandidates = []candidate{
	{"config/config.yaml", "config/config.example.yaml", manifest.FormatYAML},
	{"configs/config.yaml", "configs/config.example.yaml", manifest.FormatYAML},
	{"config.yaml", "config.example.yaml", manifest.FormatYAML},
	{"configs/config.json", "configs/config.example.json", manifest.FormatJSON},
	{"config.json", "config.example.json", manifest.FormatJSON},
}

// dotenvCandidates are ordered by which file a project treats as its real
// config. `.env.local` is an *override* layered on `.env`, not an alternative to
// it — so a project with only `.env` is complete, and demanding `.env.local`
// would be a false alarm on every Vite and Next app in the workspace.
var dotenvCandidates = []candidate{
	{".env", ".env.example", manifest.FormatDotenv},
	{".env.local", ".env.local.example", manifest.FormatDotenv},
}

// findConfigSources returns at most one config file per family: the structured
// one a Go service reads, and the dotenv one a Node app reads.
func findConfigSources(dir string) []manifest.ConfigSource {
	var out []manifest.ConfigSource
	if src, ok := pickConfig(dir, structuredCandidates); ok {
		out = append(out, src)
	}
	if src, ok := pickConfig(dir, dotenvCandidates); ok {
		out = append(out, src)
	}
	return out
}

// pickConfig returns the first candidate that exists. If none do, it returns the
// first whose template exists — a fresh clone has the template but not the
// config, and `wharf doctor` should say "copy this" rather than stay silent
// until the service dies on startup.
func pickConfig(dir string, candidates []candidate) (manifest.ConfigSource, bool) {
	for _, c := range candidates {
		if exists(dir, c.Path) {
			src := manifest.ConfigSource{Format: c.Format, Path: c.Path}
			if c.Template != "" && exists(dir, c.Template) {
				src.Template = c.Template
			}
			return src, true
		}
	}
	for _, c := range candidates {
		if c.Template != "" && exists(dir, c.Template) {
			return manifest.ConfigSource{
				Format:   c.Format,
				Path:     c.Path,
				Template: c.Template,
			}, true
		}
	}
	return manifest.ConfigSource{}, false
}

// probe reads a config file and extracts the listen port and infra needs.
func probe(dir string, src manifest.ConfigSource) probeResult {
	body := readFile(dir, src.Path)
	if body == "" && src.Template != "" {
		// Fall back to the template: it still reveals the *shape* of the
		// config (which keys exist, which datastores are used) even if the
		// values are placeholders.
		body = readFile(dir, src.Template)
	}
	if body == "" {
		return probeResult{}
	}

	switch src.Format {
	case manifest.FormatDotenv:
		return probeDotenv(body)
	case manifest.FormatJSON:
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return probeResult{}
		}
		return probeTree(v)
	default:
		var v any
		if err := yaml.Unmarshal([]byte(body), &v); err != nil {
			return probeResult{}
		}
		return probeTree(v)
	}
}

// portKeyNames are the leaf keys that plausibly hold a listen port.
var portKeyNames = map[string]bool{
	"port": true, "http_port": true, "addr": true, "http_addr": true,
	"listen": true, "listen_http": true, "address": true,
}

// portParentHints restrict which parents we trust, so a database's `port: 5432`
// is never mistaken for the service's own listen port.
var portParentHints = map[string]bool{
	"": true, "app": true, "server": true, "http": true, "api": true, "service": true,
}

// probeTree walks a decoded YAML/JSON document looking for a listen port and
// any datastore connection strings.
func probeTree(root any) probeResult {
	res := probeResult{}
	needs := map[string]manifest.Need{}

	var walk func(node any, path []string)
	walk = func(node any, path []string) {
		switch n := node.(type) {
		case map[string]any:
			for k, v := range n {
				walk(v, append(path, strings.ToLower(k)))
			}
		case map[any]any: // yaml.v3 can still produce this for non-string keys
			for k, v := range n {
				ks, ok := k.(string)
				if !ok {
					continue
				}
				walk(v, append(path, strings.ToLower(ks)))
			}
		case []any:
			for _, v := range n {
				walk(v, path)
			}
		default:
			leaf := ""
			if len(path) > 0 {
				leaf = path[len(path)-1]
			}
			parent := ""
			if len(path) > 1 {
				parent = path[len(path)-2]
			}

			// Listen port, but only under a plausible parent.
			if res.Port == 0 && portKeyNames[leaf] && portParentHints[parent] {
				if p, tmpl := parsePortShape(node); p > 0 {
					res.Port = p
					res.PortKey = strings.Join(path, ".")
					res.PortTemplate = tmpl
				}
			}

			// Datastore connection strings.
			if s, ok := node.(string); ok {
				if need, ok := needFromDSN(s); ok {
					// Keep the first DSN seen per type; configs often repeat
					// the same database under read/write aliases.
					if _, dup := needs[need.Type]; !dup {
						needs[need.Type] = need
					}
				}
			}
		}
	}
	walk(root, nil)

	// Structured (non-DSN) datastore blocks: postgres:{host,port,user,dbname}.
	for _, n := range structuredNeeds(root) {
		if _, dup := needs[n.Type]; !dup {
			needs[n.Type] = n
		}
	}

	for _, n := range needs {
		res.Needs = append(res.Needs, n)
	}
	sortNeeds(res.Needs)
	return res
}

// addrPort pulls the host and port out of ":8080" or "0.0.0.0:8080".
var addrPort = regexp.MustCompile(`^([\w.\-]*):(\d{2,5})$`)

// parsePort accepts a port as an int, a numeric string, or a listen address.
func parsePort(v any) int {
	port, _ := parsePortShape(v)
	return port
}

// parsePortShape returns the port and the template needed to write a new one
// back in the same form. Reading `":8080"` as 8080 is only half the job: writing
// 8080 back into a field that expects an address would break the service.
func parsePortShape(v any) (int, string) {
	switch t := v.(type) {
	case int:
		return validPort(t), PortPlaceholder
	case float64:
		return validPort(int(t)), PortPlaceholder
	case string:
		s := strings.TrimSpace(t)
		if m := addrPort.FindStringSubmatch(s); m != nil {
			n, _ := strconv.Atoi(m[2])
			if p := validPort(n); p > 0 {
				// m[1] is the host, empty for ":8080".
				return p, m[1] + ":" + PortPlaceholder
			}
			return 0, ""
		}
		if n, err := strconv.Atoi(s); err == nil {
			if p := validPort(n); p > 0 {
				return p, PortPlaceholder
			}
		}
	}
	return 0, ""
}

func validPort(n int) int {
	if n > 0 && n < 65536 {
		return n
	}
	return 0
}

// dsnSchemes maps a URL scheme to the infra type wharf knows how to manage.
var dsnSchemes = map[string]string{
	"postgres":     "postgres",
	"postgresql":   "postgres",
	"mysql":        "mysql",
	"redis":        "redis",
	"rediss":       "redis",
	"mongodb":      "mongo",
	"clickhouse":   "clickhouse",
	"amqp":         "rabbitmq",
	"kafka":        "kafka",
}

var dsnRe = regexp.MustCompile(`^([a-z]+)://`)

// needFromDSN recognises a connection string and classifies it.
func needFromDSN(s string) (manifest.Need, bool) {
	s = strings.TrimSpace(s)
	m := dsnRe.FindStringSubmatch(s)
	if m == nil {
		return manifest.Need{}, false
	}
	typ, ok := dsnSchemes[m[1]]
	if !ok {
		return manifest.Need{}, false
	}
	return manifest.Need{Type: typ, DSN: s}, true
}

// structuredBlocks are datastore config blocks that use discrete host/port keys
// rather than a single DSN string.
var structuredBlocks = map[string]string{
	"postgres":   "postgres",
	"postgresql": "postgres",
	"database":   "postgres",
	"db":         "postgres",
	"mysql":      "mysql",
	"redis":      "redis",
	"clickhouse": "clickhouse",
	"kafka":      "kafka",
	"mongo":      "mongo",
	"mongodb":    "mongo",
}

// structuredNeeds finds datastore blocks that have no DSN, reconstructing one
// from their parts so `wharf db` still has something to connect with.
//
// It walks the entire tree rather than only the root: real configs nest these
// blocks (under `storage:`, `datasources:`, or an environment name), and a
// root-only scan silently reports a service as needing nothing.
func structuredNeeds(root any) []manifest.Need {
	var out []manifest.Need
	seen := map[string]bool{}

	var walk func(node any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			if list, ok := node.([]any); ok {
				for _, v := range list {
					walk(v)
				}
			}
			return
		}

		// A `default:` + `connections:` pair means only one connection is live
		// and the rest are dead config. Reporting all of them would tell you a
		// service needs MySQL when it has never opened a MySQL connection.
		if active, ok := activeConnection(m); ok {
			m = active
		}

		for key, val := range m {
			block, isBlock := val.(map[string]any)
			if isBlock {
				if typ := blockType(key, block); typ != "" && !seen[typ] {
					if dsn := dsnFromBlock(typ, block); dsn != "" {
						out = append(out, manifest.Need{Type: typ, DSN: dsn})
						seen[typ] = true
						continue
					}
				}
			}
			// Not a connection block (or not one we could read): keep
			// descending. Configs nest connections under `database.connections`,
			// so bailing out at the first unrecognised level finds nothing.
			walk(val)
		}
	}
	walk(root)
	return out
}

// driverKeys are the ways a config block names its own datastore engine.
// Projects disagree on the spelling, and picking the wrong one means falling
// back to the block's key name — which labels `db: {dialect: clickhouse}` as
// Postgres purely because the block is called "db".
var driverKeys = []string{"driver", "dialect", "type", "engine", "adapter"}

// blockType decides what datastore a config block describes. An explicit driver
// field always wins over the block's key name.
func blockType(key string, b map[string]any) string {
	for _, dk := range driverKeys {
		driver, ok := b[dk].(string)
		if !ok {
			continue
		}
		if typ, known := structuredBlocks[strings.ToLower(driver)]; known {
			return typ
		}
	}
	if typ, known := structuredBlocks[strings.ToLower(key)]; known {
		return typ
	}
	return ""
}

// activeConnection narrows a `{default: "postgres", connections: {...}}` block
// to just the connection actually in use, returning it keyed by its own name so
// the caller can type it normally.
func activeConnection(m map[string]any) (map[string]any, bool) {
	def, ok := m["default"].(string)
	if !ok {
		return nil, false
	}
	conns, ok := m["connections"].(map[string]any)
	if !ok {
		return nil, false
	}
	selected, ok := conns[def].(map[string]any)
	if !ok {
		return nil, false
	}
	return map[string]any{def: selected}, true
}

// dsnFromBlock assembles a connection string from host/port/user/password/name.
func dsnFromBlock(typ string, b map[string]any) string {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := b[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
				if n := parsePort(v); n > 0 {
					return strconv.Itoa(n)
				}
			}
		}
		return ""
	}

	// A nested DSN wins outright.
	if dsn := get("dsn", "url", "uri", "connection_string", "database_url"); dsn != "" {
		if need, ok := needFromDSN(dsn); ok {
			return need.DSN
		}
	}

	// Brokers are a list, not a host: kafka: {brokers: ["localhost:9092"]}.
	// Without this, message brokers detect as needing nothing.
	if brokers := firstStringInList(b, "brokers", "hosts", "addresses", "nodes", "seeds"); brokers != "" {
		return typ + "://" + brokers
	}

	host := get("host", "hostname", "address")
	if host == "" {
		return ""
	}
	port := get("port")
	user := get("user", "username")
	pass := get("password", "pass")
	name := get("dbname", "database", "name", "db")

	scheme := typ
	if typ == "postgres" {
		scheme = "postgres"
	}

	var sb strings.Builder
	sb.WriteString(scheme + "://")
	if user != "" {
		sb.WriteString(user)
		if pass != "" {
			sb.WriteString(":" + pass)
		}
		sb.WriteString("@")
	}
	sb.WriteString(host)
	if port != "" {
		sb.WriteString(":" + port)
	}
	if name != "" {
		sb.WriteString("/" + name)
	}
	return sb.String()
}

// firstStringInList returns the first element of the first list-valued key that
// is present, e.g. the first Kafka broker address.
func firstStringInList(b map[string]any, keys ...string) string {
	for _, k := range keys {
		list, ok := b[k].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		if s, ok := list[0].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// probeDotenv handles KEY=value config.
func probeDotenv(body string) probeResult {
	res := probeResult{}
	needs := map[string]manifest.Need{}

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v == "" {
			continue
		}

		lk := strings.ToLower(k)
		if res.Port == 0 && (lk == "port" || lk == "http_port" || lk == "app_port" || lk == "server_port") {
			if p, tmpl := parsePortShape(v); p > 0 {
				res.Port = p
				res.PortKey = k
				res.PortTemplate = tmpl
			}
		}
		if need, ok := needFromDSN(v); ok {
			if _, dup := needs[need.Type]; !dup {
				needs[need.Type] = need
			}
		}
	}

	for _, n := range needs {
		res.Needs = append(res.Needs, n)
	}
	sortNeeds(res.Needs)
	return res
}

func sortNeeds(needs []manifest.Need) {
	for i := 1; i < len(needs); i++ {
		for j := i; j > 0 && needs[j].Type < needs[j-1].Type; j-- {
			needs[j], needs[j-1] = needs[j-1], needs[j]
		}
	}
}

// composeFile returns a compose file path if the project ships one.
func composeFile(dir string) string {
	return firstExisting(dir,
		"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
		filepath.Join("deploy", "docker-compose.dev.yml"),
		filepath.Join("deployments", "docker", "docker-compose.yaml"),
	)
}
