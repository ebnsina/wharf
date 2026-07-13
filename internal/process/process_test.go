package process

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// The bug this guards against: `air` and `pnpm dev` both spawn children, and
// killing only the process wharf started leaves the grandchild alive holding
// the port. The next `wharf up` then fails with "address already in use" for
// reasons that look nothing like the actual cause.
//
// Here a shell backgrounds a long sleep and waits. The sleep is a *grandchild*
// of wharf. After Stop, the entire process group must be gone.
func TestStopReapsGrandchildren(t *testing.T) {
	p := New(Spec{
		Service: "test",
		Name:    "spawner",
		Cmd:     "sleep 60 & sleep 60",
	})

	if err := p.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	pgid := p.PID()
	if pgid == 0 {
		t.Fatal("no pid after start")
	}

	// Confirm the group exists before we stop it, otherwise the assertion
	// afterwards would pass vacuously.
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("process group %d should exist before Stop: %v", pgid, err)
	}

	if err := p.Stop(2 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Signal 0 probes for existence. ESRCH means the whole group is gone —
	// including the backgrounded sleep that never received a direct signal.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return // the group is gone
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after Stop — orphaned grandchild", pgid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A process that ignores SIGTERM must still die, or its port is held forever.
func TestStopEscalatesToKill(t *testing.T) {
	p := New(Spec{
		Service: "test",
		Name:    "stubborn",
		// `trap '' TERM` makes the shell ignore SIGTERM. The busy loop matters:
		// with a trailing `sleep 60` the shell would exec into sleep as its last
		// command, discarding the trap — and sleep dies on SIGTERM, so the
		// escalation path would never run.
		Cmd: "trap '' TERM; while true; do sleep 0.1; done",
	})

	if err := p.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pgid := p.PID()

	// Let the shell install its trap. Signalling it mid-startup would kill it
	// normally and the escalation path — the thing under test — would never run.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := p.Stop(500 * time.Millisecond); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("Stop returned in %v — it cannot have waited for the grace period", elapsed)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("process ignoring SIGTERM was never SIGKILLed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTailCapturesOutput(t *testing.T) {
	p := New(Spec{Service: "test", Name: "echo", Cmd: "echo hello; echo world"})

	events := make(chan Event, 16)
	if err := p.Start(events); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for p.State() == StateRunning && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	lines := p.Tail(10)
	if len(lines) != 2 || lines[0] != "hello" || lines[1] != "world" {
		t.Fatalf("Tail = %q, want [hello world]", lines)
	}
	if p.State() != StateExited {
		t.Errorf("state = %q, want exited", p.State())
	}
}

// The ring buffer must bound memory: a dev server left running overnight logs
// indefinitely.
func TestRingBufferKeepsOnlyTheTail(t *testing.T) {
	r := newRing(3)
	for _, line := range []string{"a", "b", "c", "d", "e"} {
		r.push(line)
	}

	got := r.tail(10)
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tail = %q, want %q", got, want)
		}
	}
}
