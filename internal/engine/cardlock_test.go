package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// lockingEngine is newEngine with card locking on — the board's shape,
// where one long-lived process drives many cards and must exclude
// headless drives of the same card.
func lockingEngine(t *testing.T, ag agent.Agent) (*Engine, state.Workspace) {
	t.Helper()
	ws, store, wt := newRepo(t)
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "fake-model", MaxActive: 1,
		CardLocks: state.NewCardLocks(ws),
	})
	t.Cleanup(func() { e.Close() })
	return e, ws
}

// foreignHold stands in for another gummi process: the flock a headless
// run/resume/merge takes around its command.
func foreignHold(t *testing.T, ws state.Workspace, id domain.FeatureID) func() {
	t.Helper()
	release, err := state.AcquireLock(ws.CardLockFile(id))
	if err != nil {
		t.Fatalf("taking the foreign hold on %s: %v", id, err)
	}
	return release
}

// An autonomous run on a card a headless process already holds is
// refused before anything is spawned — the race the board used to lose
// silently, since it took no card lock at all.
func TestRunRefusesCardHeldElsewhere(t *testing.T) {
	e, ws := lockingEngine(t, agent.NewFake("hi"))
	f := feature(1, "held", domain.StageImplement)

	release := foreignHold(t, ws, f.ID)
	err := e.Run(f)
	if !errors.Is(err, state.ErrLocked) {
		t.Fatalf("Run on a card held by another process = %v, want ErrLocked", err)
	}
	if e.Get(f.ID) != nil {
		t.Fatal("a session was created for a card this engine could not lock")
	}

	// once the other process is done, the same run goes through.
	release()
	if err := e.Run(f); err != nil {
		t.Fatalf("Run after the other process released: %v", err)
	}
}

// The same for an interactive attach: the backend spawn (seconds, and a
// real process in the worktree) never happens on a card we don't hold.
func TestAttachRefusesCardHeldElsewhere(t *testing.T) {
	e, ws := lockingEngine(t, agent.NewFake("hi"))
	f := feature(1, "held", domain.StageSpec)

	release := foreignHold(t, ws, f.ID)
	if _, err := e.Attach(context.Background(), f); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("Attach on a card held by another process = %v, want ErrLocked", err)
	}
	release()

	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatalf("Attach after the other process released: %v", err)
	}
}

// While the engine drives a card, a headless drive of it is refused —
// the other half of the exclusion, and the half the board never had.
func TestDrivingHoldsTheCardLock(t *testing.T) {
	e, ws := lockingEngine(t, agent.NewFake("hi"))
	f := feature(1, "mine", domain.StageSpec)

	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if _, err := state.AcquireLock(ws.CardLockFile(f.ID)); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("a headless drive acquired a card the board is driving (%v)", err)
	}

	// dropping the session (a stage advance, a delete) frees the card.
	e.Drop(f.ID)
	release, err := state.AcquireLock(ws.CardLockFile(f.ID))
	if err != nil {
		t.Fatalf("the card lock outlived the session that held it: %v", err)
	}
	release()
}

// Pausing hands the card back: parking a run in the board so it can be
// resumed headlessly is a normal workflow, not a lock to fight.
func TestPauseReleasesTheCardLock(t *testing.T) {
	e, ws := lockingEngine(t, agent.NewFake("hi"))
	f := feature(1, "parked", domain.StageImplement)
	withWorktree(t, e.cfg.Worktrees, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)
	if err := e.Pause(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	release, err := state.AcquireLock(ws.CardLockFile(f.ID))
	if err != nil {
		t.Fatalf("a paused card is still locked: %v — a headless resume could never pick it up", err)
	}
	release()
}

// Re-running a card the engine already drives replaces the session
// without ever letting the card lock go: a headless drive must not be
// able to slip in through the gap between the two sessions.
func TestReplacingASessionKeepsTheCardLock(t *testing.T) {
	e, ws := lockingEngine(t, agent.NewFake("hi"))
	f := feature(1, "restarted", domain.StageImplement)
	withWorktree(t, e.cfg.Worktrees, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateRunning)
	if err := e.Pause(context.Background(), f.ID); err != nil {
		t.Fatal(err)
	}
	// re-run the paused card: a new session replaces the old one.
	if err := e.Run(f); err != nil {
		t.Fatalf("re-run after pause: %v", err)
	}
	waitState(t, e, f.ID, StateRunning)
	if _, err := state.AcquireLock(ws.CardLockFile(f.ID)); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("the card was unlocked while a session was driving it (%v)", err)
	}
}

// With no registry configured — the headless driver, which holds the
// card's lock around the whole command, and every test scaffold — the
// engine takes no card locks at all, so it cannot deadlock against its
// own caller.
func TestNoCardLockingByDefault(t *testing.T) {
	e := newEngine(t, agent.NewFake("hi"))
	ws := e.cfg.Workspace
	f := feature(1, "unlocked", domain.StageSpec)

	// the caller holds the card, exactly as cmd/gummi does around a drive.
	release := foreignHold(t, ws, f.ID)
	defer release()
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatalf("an engine without card locking refused a drive: %v", err)
	}
}
