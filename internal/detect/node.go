package detect

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// NodeDetector recognises Node/TypeScript projects: Vite SPAs, Next apps, and
// the build-only SDKs that must never appear as something you "start".
type NodeDetector struct{}

func (NodeDetector) Name() string { return "node" }

// packageJSON is the subset of package.json wharf reads.
type packageJSON struct {
	Name            string            `json:"name"`
	Private         bool              `json:"private"`
	Main            string            `json:"main"`
	Module          string            `json:"module"`
	Types           string            `json:"types"`
	Files           []string          `json:"files"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (d NodeDetector) Detect(dir string) (manifest.Service, bool) {
	raw := readFile(dir, "package.json")
	if raw == "" {
		return manifest.Service{}, false
	}
	var pkg packageJSON
	if err := json.Unmarshal([]byte(raw), &pkg); err != nil {
		return manifest.Service{}, false
	}

	svc := manifest.Service{
		Name:  serviceName(dir),
		Path:  dir,
		Stack: manifest.StackNode,
		Kind:  d.classify(pkg),
	}

	svc.Lifecycle = manifest.Lifecycle{
		Install: packageManager(dir) + " install",
		Build:   script(pkg, packageManager(dir), "build"),
		Test:    script(pkg, packageManager(dir), "test"),
	}

	// A library is built or watched, never served: it gets no berth, no health
	// check and no gateway route, so it can't collide with anything.
	if svc.Kind == manifest.KindLibrary {
		return svc, true
	}

	pm := packageManager(dir)
	dev := "dev"
	if _, ok := pkg.Scripts["dev"]; !ok {
		for _, alt := range []string{"start", "serve"} {
			if _, ok := pkg.Scripts[alt]; ok {
				dev = alt
				break
			}
		}
	}
	svc.Processes = []manifest.Process{{
		Name:    "web",
		Cmd:     pm + " run " + dev,
		Primary: true,
	}}

	svc.Config = findConfigSources(dir)
	svc.Berth = d.port(dir, pkg)
	if svc.Berth > 0 {
		svc.Health = &manifest.Health{Type: "http", Path: "/", TimeoutSeconds: 60}
	}
	return svc, true
}

// classify separates apps you run from libraries you publish. The tell is a
// package that declares an entrypoint for consumers (main/module/types/files)
// and has no dev server script.
func (d NodeDetector) classify(pkg packageJSON) manifest.Kind {
	hasDevServer := false
	for _, s := range []string{"dev", "start", "serve"} {
		if cmd, ok := pkg.Scripts[s]; ok && isServerCmd(cmd) {
			hasDevServer = true
			break
		}
	}
	if hasDevServer {
		return manifest.KindService
	}

	publishes := pkg.Main != "" || pkg.Module != "" || pkg.Types != "" || len(pkg.Files) > 0
	if publishes && !pkg.Private {
		return manifest.KindLibrary
	}
	// No dev server and nothing published: a watch-only build (tsc --watch,
	// tsup --watch). Still a library as far as running is concerned.
	if !hasDevServer {
		return manifest.KindLibrary
	}
	return manifest.KindService
}

// isServerCmd distinguishes `vite dev` from `tsc --watch`: only the former
// binds a port.
func isServerCmd(cmd string) bool {
	for _, marker := range []string{"vite", "next", "astro", "nuxt", "remix", "webpack serve", "node ", "nodemon", "tsx watch", "serve"} {
		if strings.Contains(cmd, marker) {
			// `vite build` is not a server; `vite` and `vite dev` are.
			if strings.Contains(cmd, "build") {
				continue
			}
			return true
		}
	}
	return false
}

// explicitPort finds `--port 5173` in a dev script.
var explicitPort = regexp.MustCompile(`--port[= ](\d{2,5})`)

// port resolves the dev-server port: an explicit flag wins, then a config file,
// then the framework's documented default.
func (d NodeDetector) port(dir string, pkg packageJSON) int {
	for _, s := range []string{"dev", "start", "serve"} {
		if cmd, ok := pkg.Scripts[s]; ok {
			if m := explicitPort.FindStringSubmatch(cmd); m != nil {
				n, _ := strconv.Atoi(m[1])
				return n
			}
		}
	}

	if cfg := findConfigSources(dir); len(cfg) > 0 {
		if res := probe(dir, cfg[0]); res.Port > 0 {
			return res.Port
		}
	}

	switch d.framework(pkg) {
	case "next":
		return 3000
	case "astro":
		return 4321
	case "nuxt":
		return 3000
	case "vite":
		return 5173
	}
	return 0
}

// framework identifies the dev server in use, which decides the default port.
func (d NodeDetector) framework(pkg packageJSON) string {
	all := map[string]string{}
	for k, v := range pkg.Dependencies {
		all[k] = v
	}
	for k, v := range pkg.DevDependencies {
		all[k] = v
	}
	for _, name := range []string{"next", "astro", "nuxt", "vite"} {
		if _, ok := all[name]; ok {
			return name
		}
	}
	return ""
}

// packageManager picks the tool the project's lockfile commits it to. Using the
// wrong one is a classic way to silently install different versions.
func packageManager(dir string) string {
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		return "pnpm"
	case exists(dir, "yarn.lock"):
		return "yarn"
	case exists(dir, "bun.lockb"), exists(dir, "bun.lock"):
		return "bun"
	default:
		return "npm"
	}
}

// script returns the command to run a package script, or "" if absent.
func script(pkg packageJSON, pm, name string) string {
	if _, ok := pkg.Scripts[name]; !ok {
		return ""
	}
	return pm + " run " + name
}
