package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// GoDetector recognises Go services. It reads .air.toml, the Makefile and the
// project's own config file rather than assuming any particular convention.
type GoDetector struct{}

func (GoDetector) Name() string { return "go" }

func (d GoDetector) Detect(dir string) (manifest.Service, bool) {
	if !exists(dir, "go.mod") {
		return manifest.Service{}, false
	}

	svc := manifest.Service{
		Name:  serviceName(dir),
		Path:  dir,
		Kind:  manifest.KindService,
		Stack: manifest.StackGo,
	}

	mk := parseMakefile(dir)
	svc.Config = findConfigSources(dir)
	svc.Processes = d.processes(dir, mk)
	svc.Lifecycle = d.lifecycle(dir, mk)

	// The config file is the source of truth for the port and the datastores.
	if len(svc.Config) > 0 {
		res := probe(dir, svc.Config[0])
		svc.Berth = res.Port
		svc.Declared = res.Port
		svc.Config[0].PortKey = res.PortKey
		svc.Config[0].PortTemplate = res.PortTemplate
		svc.Needs = res.Needs

		// Only claim -config support if the binary actually parses the flag;
		// otherwise the overlay must be injected some other way and doctor
		// should say so rather than silently producing a config nobody reads.
		if acceptsConfigFlag(dir) {
			svc.Config[0].Flag = "-config"
		}
	}

	if compose := composeFile(dir); compose != "" {
		for i := range svc.Needs {
			svc.Needs[i].Compose = compose
		}
	}

	if svc.Berth > 0 {
		svc.Health = &manifest.Health{Type: "tcp", TimeoutSeconds: 30}
	}
	return svc, true
}

// processes builds the process group. A Go service is frequently an API plus a
// worker plus a poller, each with its own Air config — they are one service.
func (d GoDetector) processes(dir string, mk makefile) []manifest.Process {
	var procs []manifest.Process

	procs = append(procs, manifest.Process{
		Name:    "api",
		Cmd:     d.primaryCmd(dir, mk),
		Primary: true,
	})

	// A Go service can carry its own SPA under web/. It is the only user
	// interface the service has — the Go server answers the API and returns 404
	// at the root — so without this the service "runs" and yet there is nothing
	// to open.
	if hasWebApp(dir) {
		pm := packageManager(filepath.Join(dir, "web"))
		procs = append(procs, manifest.Process{
			Name: "web",
			Cmd:  pm + " run dev",
			Dir:  "web",
		})
	}

	// Secondary processes, discovered from their dedicated Air configs. These
	// default to not autostarting: you rarely need the poller to iterate on an
	// HTTP handler, and starting it anyway just burns CPU and clutters logs.
	for _, role := range []string{"worker", "poller", "scheduler", "consumer"} {
		airCfg := ".air." + role + ".toml"
		switch {
		case exists(dir, airCfg):
			procs = append(procs, manifest.Process{
				Name:      role,
				Cmd:       "air -c " + airCfg,
				Autostart: boolPtr(false),
			})
		case mk.has("run-" + role):
			procs = append(procs, manifest.Process{
				Name:      role,
				Cmd:       "make run-" + role,
				Autostart: boolPtr(false),
			})
		case mk.has(role):
			procs = append(procs, manifest.Process{
				Name:      role,
				Cmd:       "make " + role,
				Autostart: boolPtr(false),
			})
		case exists(dir, filepath.Join("cmd", role)):
			procs = append(procs, manifest.Process{
				Name:      role,
				Cmd:       "go run ./cmd/" + role,
				Autostart: boolPtr(false),
			})
		}
	}
	return procs
}

// hasWebApp reports whether the project ships a front end under web/ that can
// be served in dev.
func hasWebApp(dir string) bool {
	raw := readFile(dir, filepath.Join("web", "package.json"))
	if raw == "" {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts["dev"]
	return ok
}

// primaryCmd picks how to run the main server, preferring hot-reload since that
// is what a developer wants locally.
func (d GoDetector) primaryCmd(dir string, mk makefile) string {
	// Air gives hot reload and already encodes the right subcommand args.
	if exists(dir, ".air.toml") {
		return "air"
	}
	// A Makefile target the project already documents beats anything we invent.
	for _, target := range []string{"dev", "run-api", "serve", "server", "run"} {
		if mk.has(target) {
			return "make " + target
		}
	}
	// Fall back to the conventional entrypoints.
	for _, entry := range []string{"./cmd/server", "./cmd/api", "./cmd"} {
		if exists(dir, strings.TrimPrefix(entry, "./")) {
			return "go run " + entry
		}
	}
	if exists(dir, "cmd/main.go") {
		return "go run cmd/main.go server"
	}
	return "go run ."
}

func (d GoDetector) lifecycle(dir string, mk makefile) manifest.Lifecycle {
	lc := manifest.Lifecycle{Install: "go mod download"}

	for _, t := range []string{"migrate", "migrate-up", "db-migrate"} {
		if mk.has(t) {
			lc.Migrate = "make " + t
			break
		}
	}
	if lc.Migrate == "" && exists(dir, "migrations") {
		// Most of these CLIs expose `migrate up` as a subcommand of the main
		// binary; the generated manifest is the place to correct it if not.
		lc.Migrate = "go run ./cmd migrate up"
	}

	for _, t := range []string{"seed", "db-seed"} {
		if mk.has(t) {
			lc.Seed = "make " + t
			break
		}
	}
	if mk.has("build") {
		lc.Build = "make build"
	}
	if mk.has("test") {
		lc.Test = "make test"
	} else {
		lc.Test = "go test ./..."
	}
	return lc
}

// configFlagRe matches the ways a Go program declares a --config flag across the
// stdlib flag package, cobra and urfave/cli.
var configFlagRe = regexp.MustCompile(
	`flag\.String(?:Var)?\((?:&\w+,\s*)?"config"|` +
		`StringVar\(&\w+,\s*"config"|` +
		`Name:\s*"config"|` +
		`\.String\("config"`,
)

// acceptsConfigFlag reports whether the program can be pointed at a config file
// we generate. Walks only the source directories a main package plausibly lives
// in, and stops at the first match.
func acceptsConfigFlag(dir string) bool {
	found := false
	roots := []string{"cmd", "internal", "config", "configs", "."}

	for _, root := range roots {
		if found {
			break
		}
		base := filepath.Join(dir, root)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		// Depth-limited: a match lives near the entrypoint, not in vendor.
		_ = filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
			if err != nil || found {
				return nil
			}
			if e.IsDir() {
				name := e.Name()
				if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if configFlagRe.Match(data) {
				found = true
			}
			return nil
		})
	}
	return found
}

// makefile is the set of targets a Makefile declares.
type makefile map[string]bool

func (m makefile) has(target string) bool { return m[target] }

var makeTargetRe = regexp.MustCompile(`(?m)^([a-zA-Z0-9_\-.]+):(?:[^=]|$)`)

// parseMakefile extracts target names. It ignores variable assignments and
// pattern rules, which are not things a developer runs.
func parseMakefile(dir string) makefile {
	targets := makefile{}
	body := readFile(dir, "Makefile")
	if body == "" {
		body = readFile(dir, "makefile")
	}
	for _, m := range makeTargetRe.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if name == ".PHONY" || strings.HasPrefix(name, ".") {
			continue
		}
		targets[name] = true
	}
	return targets
}
