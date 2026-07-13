// Package tui is the dashboard: a list of services on the left, the selected
// service's live log on the right.
//
// The supervisor runs on its own goroutines and pushes Events; bubbletea's model
// is only ever mutated inside Update, on the event loop. Nothing here touches a
// process directly — commands are returned as tea.Cmd and executed by the
// runtime, which keeps the render loop responsive while a service takes ten
// seconds to compile.
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ebnsina/wharf/internal/berth"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/process"
)

// Status is what the list shows for a service.
type Status int

const (
	StatusStopped Status = iota
	StatusStarting
	StatusHealthy
	StatusFailed
	// StatusForeign means something is listening on the berth that wharf did
	// not start — a stray process, or the service started by hand.
	StatusForeign
)

// pane identifies which half of the dashboard has focus.
type pane int

const (
	focusList pane = iota
	focusLogs
)

// entry is one row: a manifest plus its live state.
type entry struct {
	svc    manifest.Service
	group  *process.Group
	status Status
	logs   []string
}

// runningProcesses counts the live processes in this service's group.
func (e *entry) runningProcesses() int {
	if e.group == nil {
		return 0
	}
	n := 0
	for _, p := range e.group.Processes() {
		if p.State() == process.StateRunning {
			n++
		}
	}
	return n
}

// Model is the dashboard state.
type Model struct {
	store   *manifest.Store
	entries []*entry
	cursor  int

	viewport viewport.Model
	// spin animates services that are starting. A static glyph makes a service
	// compiling under air for ten seconds look wedged rather than busy.
	spin   spinner.Model
	events chan process.Event
	ready    bool
	width    int
	height   int
	err      error
	// focus decides which pane the arrow keys drive.
	focus pane
	// showHelp overlays the key reference.
	showHelp bool

	// view is the filtered slice of entries the list actually shows; cursor
	// indexes into it, not into entries. filter is the query behind it.
	view      []*entry
	filter    string
	filtering bool
	// following pins the log pane to the newest line, the behaviour you want
	// until you scroll up to read something.
	following bool
}

// Messages flowing into Update.
type (
	// procEvent carries supervisor output onto the event loop.
	procEvent process.Event
	// healthMsg reports the outcome of a readiness probe.
	healthMsg struct {
		service string
		ok      bool
		err     error
	}
	// tickMsg drives periodic re-probing of berths.
	tickMsg time.Time
	// errMsg surfaces a failure from a command.
	errMsg struct{ err error }
)

// SelectByName puts the cursor on a service, so opening the dashboard from
// inside a project starts on that project rather than alphabetically first.
func (m *Model) SelectByName(name string) {
	for i, e := range m.view {
		if e.svc.Name == name {
			m.cursor = i
			return
		}
	}
}

// New builds the dashboard over the given services.
func New(st *manifest.Store, services []manifest.Service) *Model {
	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = lipgloss.NewStyle().Foreground(amber)

	m := &Model{
		store:     st,
		events:    make(chan process.Event, 512),
		following: true,
		spin:      sp,
	}

	for _, svc := range services {
		if svc.Kind != manifest.KindService || svc.Disabled {
			continue
		}
		m.entries = append(m.entries, &entry{svc: svc, status: StatusStopped})
	}
	sort.Slice(m.entries, func(i, j int) bool {
		return m.entries[i].svc.Name < m.entries[j].svc.Name
	})
	m.refilter()
	return m
}

// refilter rebuilds the visible slice, keeping the cursor in range. A substring
// match is enough: these are service names a developer already half-remembers,
// not arbitrary text.
func (m *Model) refilter() {
	m.view = m.view[:0]
	q := strings.ToLower(m.filter)

	for _, e := range m.entries {
		if q == "" || strings.Contains(strings.ToLower(e.svc.Name), q) {
			m.view = append(m.view, e)
		}
	}
	if m.cursor >= len(m.view) {
		m.cursor = max(len(m.view)-1, 0)
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.waitForEvent(), m.tick(), m.probeAll(), m.spin.Tick)
}

// waitForEvent bridges the supervisor's channel into bubbletea's message loop.
// It re-arms itself on every message, which is the idiomatic way to consume a
// channel without blocking Update.
func (m *Model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		return procEvent(<-m.events)
	}
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// probeAll detects services already listening on their berth — started by hand,
// or left over from a previous run. Showing them as stopped would be a lie.
func (m *Model) probeAll() tea.Cmd {
	return func() tea.Msg {
		return tickMsg(time.Now())
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

		// Clamp: a terminal narrower than the service list — or one that reports
		// no size at all, as a bare pty does — yields negative dimensions, and
		// viewport panics on those rather than degrading.
		//
		// Rows consumed around the log body: header(1) + footer(1) +
		// pane border(2) + pane title(1).
		logWidth := max(msg.Width-listWidth-5, minPaneWidth)
		logHeight := max(msg.Height-13, minPaneHeight)

		if !m.ready {
			m.viewport = viewport.New(logWidth, logHeight)
			m.ready = true
		} else {
			m.viewport.Width, m.viewport.Height = logWidth, logHeight
		}
		m.refreshLog()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case procEvent:
		m.applyEvent(process.Event(msg))
		// Re-arm immediately: dropping this would stall all further output.
		return m, m.waitForEvent()

	case healthMsg:
		if e := m.find(msg.service); e != nil {
			if msg.ok {
				e.status = StatusHealthy
			} else {
				e.status = StatusFailed
				e.logs = append(e.logs, "wharf: "+msg.err.Error())
			}
			m.refreshLog()
		}
		return m, nil

	case spinner.TickMsg:
		// Only keep the animation running while something is actually starting;
		// an idle dashboard should not repaint four times a second forever.
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.anyStarting() {
			return m, cmd
		}
		return m, nil

	case tickMsg:
		m.reconcile()
		return m, m.tick()

	case errMsg:
		m.err = msg.err
		return m, nil
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While filtering, most keys are text. Only escape and enter get out, or the
	// letters "s" and "x" would start and stop services instead of typing.
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter = ""
			m.refilter()
			m.refreshLog()
		case "enter":
			m.filtering = false
		case "backspace":
			if m.filter != "" {
				m.filter = m.filter[:len(m.filter)-1]
				m.refilter()
				m.refreshLog()
			}
		case "up", "down":
			return m.moveCursor(msg.String())
		default:
			if len(msg.Runes) == 1 {
				m.filter += string(msg.Runes)
				m.refilter()
				m.refreshLog()
			}
		}
		return m, nil
	}

	switch msg.String() {

	case "/":
		m.filtering = true
		return m, nil

	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.refilter()
			m.refreshLog()
		}
		return m, nil

	case "q", "ctrl+c":
		// Stop everything wharf started. Leaving processes behind on quit is
		// exactly the orphan problem the supervisor exists to prevent.
		return m, tea.Sequence(m.stopAll(), tea.Quit)

	case "tab":
		if m.focus == focusList {
			m.focus = focusLogs
		} else {
			m.focus = focusList
		}

	case "?":
		m.showHelp = !m.showHelp

	case "up", "k":
		if m.focus == focusLogs {
			m.following = false
			m.viewport.LineUp(1)
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
			m.refreshLog()
		}

	case "down", "j":
		if m.focus == focusLogs {
			m.viewport.LineDown(1)
			if m.viewport.AtBottom() {
				m.following = true
			}
			return m, nil
		}
		if m.cursor < len(m.view)-1 {
			m.cursor++
			m.refreshLog()
		}

	case "home":
		m.cursor = 0
		m.refreshLog()

	case "end":
		m.cursor = max(len(m.view)-1, 0)
		m.refreshLog()

	case "s", "enter":
		// Batch the spinner tick with the start: the animation is only pumped
		// while something is starting, so it has to be woken here.
		return m, tea.Batch(m.start(m.current()), m.spin.Tick)

	case "x":
		return m, m.stop(m.current())

	case "r":
		return m, tea.Batch(
			tea.Sequence(m.stop(m.current()), m.start(m.current())),
			m.spin.Tick,
		)

	case "g":
		m.following = true
		m.viewport.GotoBottom()

	case "pgup", "b":
		m.following = false
		m.viewport.HalfViewUp()

	case "pgdown", "f":
		m.viewport.HalfViewDown()
		if m.viewport.AtBottom() {
			m.following = true
		}
	}
	return m, nil
}

// anyStarting reports whether a service is mid-startup.
func (m *Model) anyStarting() bool {
	for _, e := range m.entries {
		if e.status == StatusStarting {
			return true
		}
	}
	return false
}

// moveCursor lets the arrow keys still navigate while a filter is being typed.
func (m *Model) moveCursor(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
		}
	}
	m.refreshLog()
	return m, nil
}

// handleMouse makes the dashboard directly manipulable: click a service to
// select it, double-click to start it, and scroll whichever pane the pointer is
// over. A dashboard you can only drive blind through key chords is harder to use
// than one you can point at.
func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	overList := msg.X < listWidth

	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if overList {
			if m.cursor > 0 {
				m.cursor--
				m.refreshLog()
			}
			return m, nil
		}
		// Scrolling up in the log means you want to read something, so stop the
		// tail from yanking you back to the bottom.
		m.following = false
		m.viewport.LineUp(3)
		return m, nil

	case tea.MouseButtonWheelDown:
		if overList {
			if m.cursor < len(m.view)-1 {
				m.cursor++
				m.refreshLog()
			}
			return m, nil
		}
		m.viewport.LineDown(3)
		if m.viewport.AtBottom() {
			m.following = true
		}
		return m, nil

	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		// Clicking a pane focuses it, which is what every other pane-based UI
		// does and what a hand reaching for the mouse expects.
		if !overList {
			m.focus = focusLogs
			return m, nil
		}
		m.focus = focusList

		if i, ok := m.rowAt(msg.Y); ok {
			m.cursor = i
			m.refreshLog()
		}
		return m, nil
	}
	return m, nil
}

// rowAt maps a screen row to a service index, accounting for the header, the
// section label and the current scroll position.
func (m *Model) rowAt(y int) (int, bool) {
	// header(2) + blank(1) + label(1) + blank(1)
	const firstRow = 5

	start, end := m.window()
	i := start + (y - firstRow)
	if y < firstRow || i < start || i >= end {
		return 0, false
	}
	return i, true
}

// applyEvent folds a supervisor event into the model.
func (m *Model) applyEvent(ev process.Event) {
	e := m.find(ev.Service)
	if e == nil {
		return
	}

	switch ev.Kind {
	case process.EventLog:
		line := ev.Line
		// Only label the source when more than one process is actually running.
		// Tagging every line "[api]" for a service whose worker is not even
		// started is pure noise, repeated on every row.
		if ev.Process != "" && e.runningProcesses() > 1 {
			line = "[" + ev.Process + "] " + line
		}
		e.logs = append(e.logs, line)
		// Bound the buffer: a dev server left running overnight logs forever.
		if len(e.logs) > maxLogLines {
			e.logs = e.logs[len(e.logs)-maxLogLines:]
		}

	case process.EventExit:
		if ev.Err != nil {
			e.status = StatusFailed
			e.logs = append(e.logs, "wharf: "+ev.Process+" exited: "+ev.Err.Error())
		} else if e.group != nil && !e.group.Running() {
			e.status = StatusStopped
		}
	}

	if e == m.current() {
		m.refreshLog()
	}
}

// reconcile re-derives status from reality, so a service someone started in
// another terminal shows as occupied rather than stopped.
func (m *Model) reconcile() {
	for _, e := range m.entries {
		switch {
		case e.group != nil && e.group.Running():
			if e.status == StatusStopped {
				e.status = StatusStarting
			}
		case e.svc.Berth > 0 && berth.InUse(e.svc.Berth):
			// Not ours, but the berth is taken.
			if e.group == nil {
				e.status = StatusForeign
			}
		case e.status != StatusFailed:
			e.status = StatusStopped
		}
	}
}

// start launches a service and then probes it.
func (m *Model) start(e *entry) tea.Cmd {
	if e == nil || (e.group != nil && e.group.Running()) {
		return nil
	}

	specs := make([]process.Spec, 0, len(e.svc.Processes))
	for _, p := range e.svc.Processes {
		if !p.ShouldAutostart() {
			continue
		}
		dir := e.svc.Path
		if p.Dir != "" {
			dir = filepath.Join(e.svc.Path, p.Dir)
		}
		specs = append(specs, process.Spec{
			Service: e.svc.Name,
			Name:    p.Name,
			Cmd:     p.Cmd,
			Dir:     dir,
			Env:     p.Env,
			LogPath: m.store.LogPath(e.svc.Name, p.Name),
		})
	}
	if len(specs) == 0 {
		return func() tea.Msg {
			return errMsg{fmt.Errorf("%s has no runnable process", e.svc.Name)}
		}
	}

	e.group = process.NewGroup(e.svc.Name, specs)
	e.status = StatusStarting
	e.logs = nil

	group, svc := e.group, e.svc
	events := m.events

	return func() tea.Msg {
		if err := group.Start(events); err != nil {
			return healthMsg{service: svc.Name, ok: false, err: err}
		}
		if svc.Health == nil || svc.Berth == 0 {
			return healthMsg{service: svc.Name, ok: true}
		}

		timeout := time.Duration(svc.Health.TimeoutSeconds) * time.Second
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		err := process.WaitHealthy(ctx, process.Health{
			Type: svc.Health.Type,
			Port: svc.Berth,
			Path: svc.Health.Path,
		})
		return healthMsg{service: svc.Name, ok: err == nil, err: err}
	}
}

func (m *Model) stop(e *entry) tea.Cmd {
	if e == nil || e.group == nil {
		return nil
	}
	group := e.group
	e.status = StatusStopped
	e.group = nil

	return func() tea.Msg {
		if err := group.Stop(5 * time.Second); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// stopAll tears down every running group on quit.
func (m *Model) stopAll() tea.Cmd {
	return func() tea.Msg {
		for _, e := range m.entries {
			if e.group != nil {
				_ = e.group.Stop(5 * time.Second)
			}
		}
		return nil
	}
}

func (m *Model) current() *entry {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return m.view[m.cursor]
}

func (m *Model) find(name string) *entry {
	for _, e := range m.entries {
		if e.svc.Name == name {
			return e
		}
	}
	return nil
}

// refreshLog repaints the log pane for the selected service.
func (m *Model) refreshLog() {
	if !m.ready {
		return
	}
	e := m.current()
	if e == nil {
		m.viewport.SetContent("")
		return
	}

	lines := make([]string, len(e.logs))
	for i, l := range e.logs {
		lines[i] = colorize(l)
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))

	if m.following {
		m.viewport.GotoBottom()
	}
}

const (
	listWidth   = 34
	maxLogLines = 2000

	// The smallest pane worth drawing. Below this the dashboard says so rather
	// than rendering a mangled layout.
	minPaneWidth  = 20
	minPaneHeight = 3
	// minWidth/minHeight are the terminal dimensions the dashboard needs.
	minWidth  = listWidth + minPaneWidth + 4
	minHeight = 10
)

// tooSmall reports whether the terminal cannot fit the dashboard.
func (m *Model) tooSmall() bool {
	return m.width < minWidth || m.height < minHeight
}
