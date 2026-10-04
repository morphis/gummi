package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestBG075RebaseHandOffRunsWhileAStageIsRunning: a rebase hand-off
// dispatched while the card's stage session is still live used to be
// swallowed by the "already running" guard — the call returned nil, the
// board announced the agent was dispatched, and no rebase session ever
// started. On an autopilot card a live running session is the steady
// state, so the hand-off never worked there until the mode was switched
// off and the session had drained.
//
// The contract under test: asking for the rebase while a stage session is
// running must produce an observable rebase run — promptly, or as soon as
// the stage session drains — not a silent no-op reported as success.
func TestBG075RebaseHandOffRunsWhileAStageIsRunning(t *testing.T) {
	releaseCh := make(chan struct{})
	var rebased atomic.Bool
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "Rebase this branch onto") {
			rebased.Store(true)
			return []agent.Event{{Kind: agent.EventMessage, Text: "Rebased."}, {Kind: agent.EventIdle}}
		}
		// hold the stage session mid-turn: the steady state of a card
		// running on autopilot while the operator reaches for the rebase
		<-releaseCh
		return []agent.Event{{Kind: agent.EventMessage, Text: "working"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(func() { release(); e.Close() })

	f := feature(1, "one", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)

	// the hand-off the board's confirm dispatches, taken while the stage
	// session is live and running
	err := e.RunRebase(context.Background(), f, nil)
	if err != nil {
		t.Fatalf("BG-075: the rebase hand-off was refused outright: %v", err)
	}

	// the decided shape is interrupt-and-run: run() swaps e.live before
	// returning, so with the stage turn still held the card's live
	// session is already the rebase pass, carrying the replaced-running
	// marker settlement reads. A queue-until-drain implementation would
	// still be showing the stage session here.
	if s := e.Get(f.ID); s == nil {
		t.Fatal("BG-075: no live session for the card after the hand-off returned")
	} else if snap := s.Snapshot(); !snap.Rebase {
		t.Fatalf("BG-075: live session after the hand-off returned is not the rebase pass (rebase=%v, role=%s)", snap.Rebase, snap.Role)
	} else if !snap.ReplacedRunning {
		t.Fatal("BG-075: the rebase pass carries no replaced-running marker — settlement cannot tell an interrupted stage from a parked one")
	}

	// drain the stage session, so a hand-off that waits for the card to
	// go idle gets its chance too, and give the rebase a short window to
	// surface: an immediate dispatch lands in milliseconds, a queued one
	// the moment the stage session drains
	release()
	deadline := time.Now().Add(5 * time.Second)
	for !rebased.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !rebased.Load() {
		t.Fatal("BG-075: the rebase hand-off was reported as dispatched but no rebase session ever ran")
	}
}

// TestBG075RebaseHandOffRunsOnceTheSessionIsNotRunning is the control for
// the test above: the same hand-off, with the stage session paused first,
// goes through — isolating the live running session as the one variable
// that swallows the dispatch.
func TestBG075RebaseHandOffRunsOnceTheSessionIsNotRunning(t *testing.T) {
	var rebased atomic.Bool
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "Rebase this branch onto") {
			rebased.Store(true)
			return []agent.Event{{Kind: agent.EventMessage, Text: "Rebased."}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "working"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "one", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)
	if err := e.Pause(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StatePaused)

	if err := e.RunRebase(context.Background(), f, nil); err != nil {
		t.Fatalf("BG-075 control: the hand-off on a paused card failed: %v", err)
	}
	deadline := time.Now().Add(testWaitTimeout)
	for !rebased.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !rebased.Load() {
		t.Fatal("BG-075 control: no rebase session ran even with the card paused")
	}
}
