package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// currentService returns the service whose directory contains the working
// directory.
//
// Standing inside a project is an unambiguous statement about which service you
// mean, so wharf should not make you say it again. When several manifests match
// — a monorepo whose parent is also registered — the deepest path wins, because
// that is the more specific answer.
func currentService(services []manifest.Service) (manifest.Service, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return manifest.Service{}, false
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return manifest.Service{}, false
	}

	var best manifest.Service
	found := false

	for _, svc := range services {
		path, err := filepath.EvalSymlinks(svc.Path)
		if err != nil {
			path = svc.Path
		}
		if !under(cwd, path) {
			continue
		}
		if !found || len(path) > len(best.Path) {
			best, found = svc, true
		}
	}
	return best, found
}

// under reports whether dir is path or lives inside it.
//
// A string prefix test is not enough: "/work/api" is a prefix of "/work/api-v2"
// but is not its parent, and wharf would answer with the wrong service.
func under(dir, path string) bool {
	rel, err := filepath.Rel(path, dir)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// resolveNames turns command arguments into service names, falling back to the
// project the user is standing in.
//
// Passing no names at the root of a workspace means "all"; passing none inside a
// project means "this one". Those are both what the words mean in context.
func resolveNames(services []manifest.Service, args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}
	if svc, ok := currentService(services); ok {
		return []string{svc.Name}, nil
	}
	return nil, nil
}

// requireService resolves the single service a command applies to.
func requireService(services []manifest.Service, args []string) (manifest.Service, error) {
	if len(args) > 0 {
		for _, svc := range services {
			if svc.Name == args[0] {
				return svc, nil
			}
		}
		return manifest.Service{}, fmt.Errorf("unknown service %q", args[0])
	}

	svc, ok := currentService(services)
	if !ok {
		return manifest.Service{}, fmt.Errorf(
			"name a service, or run this from inside one")
	}
	return svc, nil
}
