package tui

import (
	"net"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/process"
)

func testModel(names ...string) *Model {
	var services []manifest.Service
	for i, n := range names {
		services = append(services, manifest.Service{
			Name:  n,
			Kind:  manifest.KindService,
			Berth: 8000 + i,
		})
	}
	m := New(nil, services)
	m.width, m.height = 120, 30
	return m
}

// typing drives the model the way a keyboard would.
func typing(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.handleKey(msg)
	}
}

func names(m *Model) []string {
	out := make([]string, 0, len(m.view))
	for _, e := range m.view {
		out = append(out, e.svc.Name)
	}
	return out
}

func TestFilterNarrowsTheList(t *testing.T) {
	m := testModel("auth-api", "billing-api", "auth-web", "search")

	typing(m, "/", "a", "u", "t", "h")

	got := names(m)
	if len(got) != 2 || got[0] != "auth-api" || got[1] != "auth-web" {
		t.Fatalf("filter %q matched %v, want [auth-api auth-web]", m.filter, got)
	}
}

// The letters that drive the dashboard must be typed, not obeyed, while a filter
// is open — otherwise typing "search" would stop a service on the "s".
func TestFilterSwallowsCommandKeys(t *testing.T) {
	m := testModel("search", "auth-api")

	typing(m, "/", "s", "e", "a", "r")

	if m.filter != "sear" {
		t.Fatalf("filter = %q, want \"sear\" — a command key was obeyed instead of typed", m.filter)
	}
	if got := names(m); len(got) != 1 || got[0] != "search" {
		t.Fatalf("view = %v, want [search]", got)
	}
}

func TestEscapeClearsTheFilter(t *testing.T) {
	m := testModel("auth-api", "billing-api")

	typing(m, "/", "a", "u", "t", "h")
	if len(m.view) != 1 {
		t.Fatalf("filter did not apply: %v", names(m))
	}

	typing(m, "esc")
	if m.filter != "" || len(m.view) != 2 {
		t.Fatalf("escape left filter=%q view=%v, want empty and all services", m.filter, names(m))
	}
}

func TestBackspaceWidensTheFilter(t *testing.T) {
	m := testModel("auth-api", "auth-web", "billing")

	typing(m, "/", "a", "u", "t", "h", "-", "w")
	if len(m.view) != 1 {
		t.Fatalf("expected one match, got %v", names(m))
	}

	typing(m, "backspace", "backspace")
	if len(m.view) != 2 {
		t.Fatalf("backspace should have widened to two matches, got %v", names(m))
	}
}

// A filter that empties the list must not leave the cursor pointing past the
// end — current() would index out of range and panic on the next repaint.
func TestFilterClampsCursor(t *testing.T) {
	m := testModel("auth-api", "billing-api", "search")

	typing(m, "down", "down") // cursor at 2
	typing(m, "/", "a", "u", "t", "h")

	if m.cursor >= len(m.view) {
		t.Fatalf("cursor %d is past the end of a %d-item view", m.cursor, len(m.view))
	}
	if m.current() == nil {
		t.Fatal("current() is nil after filtering")
	}
}

func TestFilterWithNoMatchIsSafe(t *testing.T) {
	m := testModel("auth-api", "billing-api")

	typing(m, "/", "z", "z", "z")

	if len(m.view) != 0 {
		t.Fatalf("expected no matches, got %v", names(m))
	}
	if m.current() != nil {
		t.Fatal("current() should be nil when nothing matches")
	}
	// The view must still render rather than panic.
	_ = m.View()
}

// A click maps to the service under the pointer, accounting for the header rows
// above the list.
func TestRowAtMapsClicksToServices(t *testing.T) {
	m := testModel("a", "b", "c", "d")

	// firstRow is 5: header(2) + blank + label + blank.
	if i, ok := m.rowAt(5); !ok || i != 0 {
		t.Errorf("row 5 = (%d,%v), want the first service", i, ok)
	}
	if i, ok := m.rowAt(7); !ok || i != 2 {
		t.Errorf("row 7 = (%d,%v), want the third service", i, ok)
	}
	if _, ok := m.rowAt(2); ok {
		t.Error("a click in the header should not select a service")
	}
	if _, ok := m.rowAt(99); ok {
		t.Error("a click past the last service should select nothing")
	}
}

// The bug that made the dashboard contradict its own logs.
//
// A slow first compile outlasts the readiness probe, so the service is marked
// failed. Then reconcile only ever promoted a *stopped* service, so once failed
// it stayed failed — while the log beside it said "listening and serving HTTP".
// The label has to be able to disagree with the past.
func TestReconcileRecoversFromAFailedProbe(t *testing.T) {
	m := testModel("slow-api")
	e := m.entries[0]

	// It was marked failed when the probe timed out...
	e.status = StatusFailed
	e.failure = "still not listening on port 8088"

	// ...but the process is alive and now answering on its berth.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	e.svc.Berth = ln.Addr().(*net.TCPAddr).Port
	e.group = process.NewGroup("slow-api", []process.Spec{
		{Service: "slow-api", Name: "api", Cmd: "sleep 30"},
	})
	if err := e.group.Start(nil); err != nil {
		t.Fatal(err)
	}
	defer e.group.Stop(2 * time.Second)

	m.reconcile()

	if e.status != StatusHealthy {
		t.Errorf("status = %v, want healthy — the process is running and the berth answers", e.status)
	}
	if e.failure != "" {
		t.Errorf("failure = %q, want it cleared once the service recovered", e.failure)
	}
}

// A service that really is dead must stay marked failed.
func TestReconcileKeepsARealFailureFailed(t *testing.T) {
	m := testModel("broken-api")
	e := m.entries[0]
	e.status = StatusFailed
	e.failure = "the process exited without listening on port 9999"
	e.svc.Berth = 9999 // nothing is listening

	m.reconcile()

	if e.status != StatusFailed {
		t.Errorf("status = %v, want it to stay failed — nothing is running", e.status)
	}
}
