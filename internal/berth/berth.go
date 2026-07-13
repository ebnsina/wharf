// Package berth assigns each service a stable port it owns.
//
// The name is the point: a berth is a numbered slot, and two ships cannot
// occupy one. Detected defaults collide constantly — several services in a
// typical workspace all ship with :8080 — so wharf treats the detected port as
// a *preference* and the allocated berth as the truth. Everything downstream
// (config overlay, gateway routes, dependent services' API URLs) reads the
// berth, which is why a port change can never silently desync from a route.
package berth

import (
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Allocation is the outcome for one service.
type Allocation struct {
	Service string
	// Preferred is what the project's own config asked for.
	Preferred int
	// Berth is what wharf assigned. Equal to Preferred when uncontested.
	Berth int
	// Moved is true when the service had to be relocated off its preference.
	Moved bool
	// Reason explains a move, for the user.
	Reason string
}

// Allocate assigns berths across all services deterministically.
//
// Determinism matters more than optimality here: the same workspace must always
// produce the same berths, or a config generated yesterday would point at the
// wrong port today. Services are processed in name order, and the first claimant
// of a port keeps it.
func Allocate(services []manifest.Service, r manifest.BerthRange) []Allocation {
	sorted := make([]manifest.Service, len(services))
	copy(sorted, services)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	taken := map[int]string{}
	allocs := make([]Allocation, 0, len(sorted))

	// Pass 1: honour uncontested preferences. A service that already owns a
	// unique port keeps it, so the common case involves no change at all and
	// existing bookmarks, curl commands and gateway routes keep working.
	for _, svc := range sorted {
		if !needsBerth(svc) {
			continue
		}
		a := Allocation{Service: svc.Name, Preferred: svc.Berth, Berth: svc.Berth}
		if svc.Berth > 0 && taken[svc.Berth] == "" {
			taken[svc.Berth] = svc.Name
		}
		allocs = append(allocs, a)
	}

	// Pass 2: relocate the losers of any contest.
	for i := range allocs {
		a := &allocs[i]
		if a.Berth > 0 && taken[a.Berth] == a.Service {
			continue // uncontested winner
		}

		next := freePort(taken, r)
		if next == 0 {
			a.Reason = fmt.Sprintf("no free berth in range %d-%d", r.From, r.To)
			continue
		}

		switch {
		case a.Preferred == 0:
			a.Reason = "no port declared in config"
		default:
			a.Reason = fmt.Sprintf("port %d already claimed by %s", a.Preferred, taken[a.Preferred])
		}
		a.Berth = next
		a.Moved = true
		taken[next] = a.Service
	}

	return allocs
}

// needsBerth reports whether a service listens at all. Libraries do not.
func needsBerth(svc manifest.Service) bool {
	return svc.Kind == manifest.KindService
}

// freePort returns the lowest unallocated port in the range that is also not
// currently bound by some process outside wharf's control.
func freePort(taken map[int]string, r manifest.BerthRange) int {
	for p := r.From; p <= r.To; p++ {
		if taken[p] != "" {
			continue
		}
		if InUse(p) {
			continue
		}
		return p
	}
	return 0
}

// InUse reports whether something is already listening on a port. This catches
// the case wharf cannot see in any manifest: a stray process, a Docker
// container, or a service someone started by hand.
func InUse(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Conflict is two or more services wanting the same declared port.
type Conflict struct {
	Port     int
	Services []string
}

// Conflicts reports every contested port among the services' *declared*
// preferences. This is what `wharf doctor` shows: it describes the problem in
// the projects, independent of how wharf chose to resolve it.
func Conflicts(services []manifest.Service) []Conflict {
	byPort := map[int][]string{}
	for _, svc := range services {
		if !needsBerth(svc) || svc.Berth == 0 {
			continue
		}
		byPort[svc.Berth] = append(byPort[svc.Berth], svc.Name)
	}

	var out []Conflict
	for port, names := range byPort {
		if len(names) < 2 {
			continue
		}
		sort.Strings(names)
		out = append(out, Conflict{Port: port, Services: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}
