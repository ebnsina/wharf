// Package process supervises the commands that make up a service.
//
// The central problem is orphans. A dev command is never a single process:
// `air` compiles and then execs your binary; `pnpm run dev` spawns vite through
// a shell. Killing only the process wharf spawned leaves the grandchild alive,
// still holding the port — and the next `wharf up` fails with "address already
// in use" for reasons that appear to have nothing to do with wharf.
//
// So every process is started in its own process group and signalled as a
// group, which reaches the whole tree.
package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// State is the lifecycle of a supervised process.
type State string

const (
	StateStopped State = "stopped"
	StateRunning State = "running"
	StateExited  State = "exited"
	StateFailed  State = "failed"
)

// Event is emitted as a process runs, so a caller (CLI or TUI) can react
// without polling.
type Event struct {
	Service string
	Process string
	// Line is a line of output, when Kind is EventLog.
	Line string
	Kind EventKind
	// Err is set when Kind is EventExit and the process failed.
	Err error
}

// EventKind distinguishes output from lifecycle transitions.
type EventKind int

const (
	EventLog EventKind = iota
	EventStarted
	EventExit
)

// Spec is everything needed to run one process.
type Spec struct {
	Service string
	Name    string
	// Cmd is a shell command line, so a manifest can say `air -c .air.worker.toml`
	// exactly as a developer would type it.
	Cmd string
	Dir string
	Env map[string]string
	// LogPath persists output; the ring buffer keeps only the tail in memory.
	LogPath string
}

// Process is a single supervised command.
type Process struct {
	Spec Spec

	mu    sync.RWMutex
	state State
	pid   int
	cmd   *exec.Cmd
	log   *ring
	logFD *os.File
}

// New prepares a process. It does not start it.
func New(spec Spec) *Process {
	return &Process{
		Spec:  spec,
		state: StateStopped,
		log:   newRing(500),
	}
}

// State reports the current lifecycle state.
func (p *Process) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// PID reports the process group leader's pid, or 0.
func (p *Process) PID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pid
}

// Tail returns the most recent output lines.
func (p *Process) Tail(n int) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.log.tail(n)
}

// Start launches the process in its own process group and streams its output to
// events. events may be nil.
func (p *Process) Start(events chan<- Event) error {
	p.mu.Lock()
	if p.state == StateRunning {
		p.mu.Unlock()
		return fmt.Errorf("%s/%s is already running", p.Spec.Service, p.Spec.Name)
	}

	// A shell is used deliberately: manifests hold the command line a developer
	// would type, including pipes and flags, not an argv array.
	cmd := exec.Command("sh", "-c", p.Spec.Cmd)
	cmd.Dir = p.Spec.Dir
	cmd.Env = environ(p.Spec.Env)

	// Setpgid puts the child — and everything it spawns — into a new process
	// group whose id equals the child's pid. Signalling -pid then reaches the
	// entire tree, which is the only reliable way to stop `air` or `pnpm dev`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := p.openLog(); err != nil {
		p.mu.Unlock()
		return err
	}

	if err := cmd.Start(); err != nil {
		p.state = StateFailed
		p.closeLog()
		p.mu.Unlock()
		return fmt.Errorf("start %s: %w", p.Spec.Cmd, err)
	}

	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.state = StateRunning
	p.mu.Unlock()

	emit(events, Event{Service: p.Spec.Service, Process: p.Spec.Name, Kind: EventStarted})

	go p.pump(stdout, events)
	go p.reap(events)
	return nil
}

// pump forwards output to the ring buffer, the log file and the event channel.
func (p *Process) pump(r io.Reader, events chan<- Event) {
	scanner := bufio.NewScanner(r)
	// Dev servers emit long lines (stack traces, bundled asset lists); the
	// default 64KiB limit would truncate them mid-line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		p.mu.Lock()
		p.log.push(line)
		if p.logFD != nil {
			fmt.Fprintln(p.logFD, line)
		}
		p.mu.Unlock()

		emit(events, Event{
			Service: p.Spec.Service,
			Process: p.Spec.Name,
			Kind:    EventLog,
			Line:    line,
		})
	}
}

// reap waits for exit and records why.
func (p *Process) reap(events chan<- Event) {
	err := p.cmd.Wait()

	p.mu.Lock()
	switch {
	case err == nil:
		p.state = StateExited
	case isSignalled(err):
		// We stopped it on purpose; that is not a failure.
		p.state = StateStopped
		err = nil
	default:
		p.state = StateFailed
	}
	p.pid = 0
	p.closeLog()
	p.mu.Unlock()

	emit(events, Event{
		Service: p.Spec.Service,
		Process: p.Spec.Name,
		Kind:    EventExit,
		Err:     err,
	})
}

// Stop signals the whole process group, escalating to SIGKILL if the group does
// not exit in time. A dev server that ignores SIGTERM would otherwise keep its
// port forever.
func (p *Process) Stop(grace time.Duration) error {
	p.mu.RLock()
	pid, state := p.pid, p.state
	p.mu.RUnlock()

	if state != StateRunning || pid == 0 {
		return nil
	}

	// The negative pid targets the process group, not just the leader.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return fmt.Errorf("terminate %s/%s: %w", p.Spec.Service, p.Spec.Name, err)
	}

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if p.State() != StateRunning {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill %s/%s: %w", p.Spec.Service, p.Spec.Name, err)
	}

	// Wait for reap to record the exit. Without this, Stop can return while the
	// state still reads "running", and a caller that immediately restarts the
	// service would be refused for a process that is already dead.
	p.awaitExit(2 * time.Second)
	return nil
}

// awaitExit blocks until the process leaves the running state, or the timeout
// elapses. Bounded so a wedged reaper can never hang the caller.
func (p *Process) awaitExit(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.State() != StateRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *Process) openLog() error {
	if p.Spec.LogPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p.Spec.LogPath), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	// Truncate: the log is the current run, not an archive. Scrolling past a
	// previous run's output to find the current error is its own small misery.
	f, err := os.Create(p.Spec.LogPath)
	if err != nil {
		return fmt.Errorf("open log %s: %w", p.Spec.LogPath, err)
	}
	p.logFD = f
	return nil
}

func (p *Process) closeLog() {
	if p.logFD != nil {
		_ = p.logFD.Close()
		p.logFD = nil
	}
}

// isSignalled reports whether the process died from a signal we sent.
func isSignalled(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	status, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return status.Signaled()
}

// environ merges extra variables over the parent environment.
func environ(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// emit sends without blocking: a slow or absent consumer must never stall a
// service's output pump.
func emit(ch chan<- Event, ev Event) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
}

// ring is a fixed-size circular buffer of recent log lines, so a service that
// logs forever cannot exhaust memory.
type ring struct {
	buf   []string
	next  int
	full  bool
	limit int
}

func newRing(limit int) *ring {
	return &ring{buf: make([]string, limit), limit: limit}
}

func (r *ring) push(line string) {
	r.buf[r.next] = line
	r.next = (r.next + 1) % r.limit
	if r.next == 0 {
		r.full = true
	}
}

// tail returns up to n lines, oldest first.
func (r *ring) tail(n int) []string {
	size := r.next
	if r.full {
		size = r.limit
	}
	if n > size {
		n = size
	}
	out := make([]string, 0, n)
	for i := size - n; i < size; i++ {
		idx := i
		if r.full {
			idx = (r.next + i) % r.limit
		}
		out = append(out, r.buf[idx])
	}
	return out
}
