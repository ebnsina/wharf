package detect

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A TCP dial only proves a port is open. A Go server binds its listener before
// its dependencies are wired, so the port answers while the app is still
// starting — and a request fired at that moment fails for reasons nothing in the
// output explains.
//
// An HTTP probe against the service's own health endpoint proves it is serving.
// Every service names that endpoint differently, so wharf reads it out of the
// code rather than picking one path and applying it to all of them.

var (
	// groupRe matches a router group and the prefix it mounts under:
	//   v1 := api.router.Group("/v1")
	//   users := v1.Group("/users", middleware)
	groupRe = regexp.MustCompile(`(\w+)\s*:?=\s*([\w.]+)\.Group\(\s*"([^"]*)"`)

	// routeRe matches a route registered on a router or a group.
	routeRe = regexp.MustCompile(`(\w+)\.(?:GET|Get|HandleFunc|Handle)\(\s*"(/[A-Za-z0-9/_.\-]*)"`)

	// compareRe catches a hand-rolled router, which compares the path rather
	// than registering it: `if method == "GET" && path == "/health"`. Matching
	// this safely depends entirely on the leaf-name filter — without it every
	// string comparison against a path would look like a route.
	compareRe = regexp.MustCompile(`==\s*"(/[A-Za-z0-9/_.\-]*)"`)
)

// healthNames are the leaf segments that mean "am I alive", ranked. A path is a
// health endpoint only if its *last* segment is one of these, which is what
// keeps /network-health, /ban/status and /test-custom-status out — those are
// features that merely contain a health-ish word.
var healthNames = []string{
	"healthz",
	"health-check",
	"healthcheck",
	"health",
	"livez",
	"readyz",
	"ping",
}

// rank orders candidates. A dedicated liveness path beats a versioned one, and
// anything beats /ping, which plenty of services answer without meaning it.
func rank(p string) int {
	leaf := p[strings.LastIndex(p, "/")+1:]
	for i, name := range healthNames {
		if strings.EqualFold(leaf, name) {
			// Prefer the shallower path: /health is more likely the liveness
			// endpoint than /v1/health, which may be an API resource.
			return i*10 + strings.Count(p, "/")
		}
	}
	return -1
}

// HealthPath finds the endpoint a Go service exposes to say it is alive, or ""
// when it has none.
//
// Router groups are resolved, because the literal in the source is not the path
// the server serves:
//
//	v1 := api.router.Group("/v1")
//	v1.GET("/health-check", ...)   // really /v1/health-check
//
// Reading the literal alone yields a path that 404s. Reporting an endpoint that
// does not exist is worse than reporting none.
func HealthPath(dir string) string {
	var found []string
	seen := map[string]bool{}

	add := func(p string) {
		if rank(p) < 0 || seen[p] {
			return
		}
		seen[p] = true
		found = append(found, p)
	}

	for _, root := range []string{"internal", "cmd", "api", "pkg", "routes", "."} {
		base := filepath.Join(dir, root)
		if _, err := os.Stat(base); err != nil {
			continue
		}

		_ = filepath.WalkDir(base, func(file string, e os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() {
				name := e.Name()
				if name == "vendor" || name == "node_modules" || name == "web" ||
					strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				return nil
			}

			data, err := os.ReadFile(file)
			if err != nil {
				return nil
			}
			body := string(data)

			prefixes := groupPrefixes(body)

			for _, m := range routeRe.FindAllStringSubmatch(body, -1) {
				receiver, p := m[1], m[2]
				add(join(prefixes[receiver], p))
			}
			for _, m := range compareRe.FindAllStringSubmatch(body, -1) {
				add(m[1])
			}
			return nil
		})
	}

	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool { return rank(found[i]) < rank(found[j]) })
	return found[0]
}

// groupPrefixes maps each router variable to the path it is mounted under.
//
// Groups nest, so the map is resolved repeatedly until it stops changing —
// `users := v1.Group("/users")` cannot be resolved before v1 itself is.
type groupDecl struct{ name, parent, prefix string }

func groupPrefixes(body string) map[string]string {
	var decls []groupDecl
	for _, m := range groupRe.FindAllStringSubmatch(body, -1) {
		decls = append(decls, groupDecl{name: m[1], parent: m[2], prefix: m[3]})
	}

	prefixes := map[string]string{}

	// Bounded: each pass resolves at least one more level of nesting, and no
	// real router nests deeper than a handful.
	for range decls {
		changed := false
		for _, d := range decls {
			if _, done := prefixes[d.name]; done {
				continue
			}
			// A parent that is not itself a known group is the root router, and
			// contributes no prefix.
			parent, isGroup := prefixes[d.parent]
			if !isGroup && isGroupName(decls, d.parent) {
				continue // its parent is a group we have not resolved yet
			}
			prefixes[d.name] = join(parent, d.prefix)
			changed = true
		}
		if !changed {
			break
		}
	}
	return prefixes
}

// isGroupName reports whether a receiver is itself a declared group.
func isGroupName(decls []groupDecl, name string) bool {
	for _, d := range decls {
		if d.name == name {
			return true
		}
	}
	return false
}

// join concatenates a group prefix and a route path.
func join(prefix, p string) string {
	if prefix == "" {
		return p
	}
	return path.Join(prefix, p)
}
