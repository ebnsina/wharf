// Package orchestrator turns manifests into running services: it orders them by
// dependency, checks their infrastructure, puts each on its berth, and starts
// them.
package orchestrator

import (
	"fmt"
	"sort"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Resolve returns the services needed to run the named ones, ordered so that a
// dependency always precedes its dependent.
//
// Unknown names are an error rather than a silent omission: starting four of the
// five services you asked for, and saying nothing, is worse than refusing.
func Resolve(all []manifest.Service, names []string) ([]manifest.Service, error) {
	index := map[string]manifest.Service{}
	for _, s := range all {
		index[s.Name] = s
	}

	// No names means every enabled service.
	if len(names) == 0 {
		for _, s := range all {
			if s.Kind == manifest.KindService && !s.Disabled {
				names = append(names, s.Name)
			}
		}
		sort.Strings(names)
	}

	for _, n := range names {
		if _, ok := index[n]; !ok {
			return nil, fmt.Errorf("unknown service %q", n)
		}
	}

	var ordered []manifest.Service
	state := map[string]mark{}

	var visit func(name string, stack []string) error
	visit = func(name string, stack []string) error {
		switch state[name] {
		case marked:
			return nil
		case visiting:
			// A cycle would otherwise recurse until the stack blew. Naming the
			// cycle is the only useful thing to say here.
			return fmt.Errorf("dependency cycle: %v", append(stack, name))
		}

		state[name] = visiting
		svc := index[name]

		for _, dep := range svc.DependsOn {
			if _, ok := index[dep]; !ok {
				return fmt.Errorf("%s depends on unknown service %q", name, dep)
			}
			if err := visit(dep, append(stack, name)); err != nil {
				return err
			}
		}

		state[name] = marked
		ordered = append(ordered, svc)
		return nil
	}

	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

type mark int

const (
	unvisited mark = iota
	visiting
	marked
)
