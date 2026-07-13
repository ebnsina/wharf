// Package detect infers how a project runs by reading the files it already has.
// It is deliberately ignorant of any particular workspace: a Detector sees a
// directory and reports what it can prove, so pointing wharf at a new machine
// or a client's repo works with no code change.
package detect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Detector recognises one kind of project. Adding a language means adding a
// Detector and registering it — no caller changes.
type Detector interface {
	// Name identifies the detector in diagnostics.
	Name() string
	// Detect returns a populated Service if dir looks like this kind of
	// project, or ok=false to pass.
	Detect(dir string) (manifest.Service, bool)
}

// registry is ordered: the first detector that claims a directory wins. Go
// precedes Node because several Go services embed a Vite SPA under web/, and
// the Go server is the thing you actually run.
var registry = []Detector{
	GoDetector{},
	PythonDetector{},
	NodeDetector{},
}

// Scan walks the immediate children of root and detects a Service for each.
// It descends one extra level into directories that are plainly monorepos
// (no project marker of their own, but children that have one), which is how
// a monorepo like acme/{acme-api,acme-web} is picked up.
func Scan(root string) ([]manifest.Service, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var services []manifest.Service
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())

		if svc, ok := detectOne(dir); ok {
			services = append(services, svc)
			continue
		}
		// No marker here — try one level down before giving up.
		services = append(services, scanChildren(dir)...)
	}

	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services, nil
}

// scanChildren handles the monorepo case: a plain directory whose children are
// the real projects.
func scanChildren(dir string) []manifest.Service {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	parent := filepath.Base(dir)

	var out []manifest.Service
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		svc, ok := detectOne(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		svc.Name = qualify(parent, svc.Name)
		out = append(out, svc)
	}
	return out
}

// qualify namespaces a monorepo child under its parent. Without this, generic
// child names ("backend", "web", "frontend") collide across repos and one
// manifest silently overwrites another.
//
// The parent prefix is skipped when the child already carries it, so
// acme/acme-api stays acme-api rather than becoming
// acme-acme-api.
func qualify(parent, child string) string {
	if strings.HasPrefix(child, parent) {
		return child
	}
	return parent + "-" + child
}

// detectOne runs the registry against a single directory.
func detectOne(dir string) (manifest.Service, bool) {
	for _, d := range registry {
		if svc, ok := d.Detect(dir); ok {
			return svc, true
		}
	}
	return manifest.Service{}, false
}

// --- shared helpers used by every detector ---

// exists reports whether a path exists relative to dir.
func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

// firstExisting returns the first of rels that exists, relative to dir.
func firstExisting(dir string, rels ...string) string {
	for _, rel := range rels {
		if exists(dir, rel) {
			return rel
		}
	}
	return ""
}

// readFile reads a file relative to dir, returning "" when absent. Detection is
// best-effort: an unreadable file means "cannot prove", never a hard failure.
func readFile(dir, rel string) string {
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return ""
	}
	return string(data)
}

// serviceName derives a manifest name from a directory path.
func serviceName(dir string) string {
	return filepath.Base(dir)
}

// boolPtr is a small convenience for the Autostart tri-state.
func boolPtr(b bool) *bool { return &b }
