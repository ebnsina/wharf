package process

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Group is a service's processes supervised as one unit. An API and its worker
// are started together and, more importantly, stopped together: leaving a worker
// running after its API is gone produces a service that half-exists.
type Group struct {
	Service string

	mu    sync.RWMutex
	procs []*Process
}

// NewGroup builds a group from specs.
func NewGroup(service string, specs []Spec) *Group {
	g := &Group{Service: service}
	for _, s := range specs {
		g.procs = append(g.procs, New(s))
	}
	return g
}

// Processes returns the supervised processes.
func (g *Group) Processes() []*Process {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*Process, len(g.procs))
	copy(out, g.procs)
	return out
}

// Start launches every process in the group.
func (g *Group) Start(events chan<- Event) error {
	for _, p := range g.Processes() {
		if err := p.Start(events); err != nil {
			// Roll back: a partially started group is worse than none, because
			// the surviving half holds ports and confuses the next attempt.
			_ = g.Stop(5 * time.Second)
			return err
		}
	}
	return nil
}

// Stop signals every process, collecting errors rather than bailing at the
// first: a failure to stop one process must not leave the others running.
func (g *Group) Stop(grace time.Duration) error {
	var firstErr error
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, p := range g.Processes() {
		wg.Add(1)
		go func(p *Process) {
			defer wg.Done()
			if err := p.Stop(grace); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return firstErr
}

// Running reports whether any process in the group is alive.
func (g *Group) Running() bool {
	for _, p := range g.Processes() {
		if p.State() == StateRunning {
			return true
		}
	}
	return false
}

// Health describes how to tell that a service is actually serving, as opposed
// to merely having a live process. A Go service under `air` spends its first
// seconds compiling; its process is up long before its port is.
type Health struct {
	// Type is "tcp" or "http".
	Type string
	Port int
	Path string
}

// WaitHealthy blocks until the service answers, its processes die, or ctx is
// done.
//
// alive reports whether the service's processes are still running; it may be
// nil. Watching it is what makes the wait honest: a Go service under `air` spends
// its first minute compiling, and a stopwatch cannot tell that from a hang. A
// process that is still working is not a failure — a process that has exited
// without opening its port is, and that is worth saying immediately rather than
// making the user wait out a timeout for an answer already known.
func WaitHealthy(ctx context.Context, h Health, alive func() bool) error {
	if h.Port == 0 {
		return nil // nothing to probe
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		if probe(h) {
			return nil
		}
		if alive != nil && !alive() {
			return fmt.Errorf("the process exited without listening on port %d", h.Port)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"still not listening on port %d after %s — it may only be slow to build",
				h.Port, timeoutOf(ctx))
		case <-ticker.C:
		}
	}
}

// timeoutOf renders how long a context allowed, for the message above.
func timeoutOf(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline).Truncate(time.Second) * -1
	}
	return 0
}

func probe(h Health) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", h.Port)

	if h.Type == "http" {
		path := h.Path
		if path == "" {
			path = "/"
		}
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		// Any response at all proves the server is listening and routing. A 404
		// still means it is up, so status is deliberately not checked.
		return true
	}

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
