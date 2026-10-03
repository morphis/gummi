package engine

import (
	"context"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestBG026PauseLeavesBusySet locks in that a mid-turn Pause clears Busy():
// Session.stop() now clears busy unconditionally inside its sync.Once, so
// every stop path (Pause, Drop, replace, quit) gets it for free.
func TestBG026PauseLeavesBusySet(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		close(started)
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { close(release); e.Close() })

	f1 := feature(1, "one", domain.StageImplement)
	withWorktree(t, wt, f1)
	if err := e.Run(f1); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateRunning)
	<-started // mid-turn: setBusy(true) has definitely already run

	if err := e.Pause(context.Background(), "FD-001"); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StatePaused)
	if e.Get("FD-001").Busy() {
		t.Fatal("BG-026: Pause left Busy() true")
	}
}

// TestBG026BackendDeathLeavesRunRetryable locks in that a backend death (the
// agent's event stream closing without a terminal event) routes through
// failRun: busy clears, the session lands in StatePaused, and the card can
// be run again rather than being refused as still in flight.
func TestBG026BackendDeathLeavesRunRetryable(t *testing.T) {
	ag := &agent.Fake{DieAfter: 1}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := autopilotFeature(1, "one")
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventError)

	if e.Get("FD-001").Busy() {
		t.Fatal("BG-026: backend death left Busy() true")
	}
	if st := e.Get("FD-001").State(); st != StatePaused {
		t.Fatalf("BG-026: backend death left the run %s, want paused", st)
	}
	if err := e.Run(f); err != nil {
		t.Fatalf("BG-026: a paused run could not be run again: %v", err)
	}
	waitFor(t, e, EventError)
}
