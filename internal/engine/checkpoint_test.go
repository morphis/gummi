package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestAutonomousTurnCheckpointsWorktree: an implement run that leaves
// uncommitted edits behind gets them committed to the feature branch as
// the turn completes, so agent work is never stranded uncommitted.
func TestAutonomousTurnCheckpointsWorktree(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		// the "agent" writes code but never commits it
		if err := os.WriteFile(filepath.Join(opts.WorkDir, "feat.go"), []byte("package x\n"), 0o600); err != nil {
			t.Error(err)
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Implemented."},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	// EventIdle follows the checkpoint commit; waitState(Done) would return
	// mid-commit and race git on the repo's index.lock.
	waitFor(t, e, EventIdle)

	ctx := context.Background()
	if dirty, err := wt.Dirty(ctx, &f); dirty || err != nil {
		t.Errorf("worktree still dirty after turn end: %v %v", dirty, err)
	}
	p, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", p, "log", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "FD-001: implement checkpoint" {
		t.Errorf("checkpoint subject = %q", got)
	}
	snap := e.Get("FD-001").Snapshot()
	if !containsLine(snap.Activity, "worktree committed: FD-001: implement checkpoint") {
		t.Errorf("activity missing checkpoint line: %v", snap.Activity)
	}
}

// TestCleanTurnAddsNoCheckpoint: a turn that changed nothing must not
// manufacture an empty commit or an activity line.
func TestCleanTurnAddsNoCheckpoint(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Nothing to do."},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle) // idle follows any checkpoint work

	snap := e.Get("FD-001").Snapshot()
	for _, a := range snap.Activity {
		if strings.Contains(a, "checkpoint") {
			t.Errorf("clean turn produced checkpoint activity: %q", a)
		}
	}
}

// TestCheckpointWorktreeGoneFailsRunWithoutIdle: when the worktree
// directory disappears out from under an active implement turn (an
// environment/filesystem glitch, not a clean Remove), the checkpoint
// commit fails with "no worktree" — total loss, not the
// "leftovers are still on disk" case checkpoint's best-effort swallow is
// meant for. That must fail the run (EventError, StatePaused) instead of
// reading as a clean finish (EventIdle): a clean finish is exactly what
// let the driver step implement straight into review with no worktree
// left for it to look at.
func TestCheckpointWorktreeGoneFailsRunWithoutIdle(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		// the agent's own tool calls would start failing against the
		// missing path in reality; the fake models the end state directly —
		// the worktree is gone by the time the turn goes idle.
		if err := os.RemoveAll(opts.WorkDir); err != nil {
			t.Error(err)
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "done"},
			{Kind: agent.EventIdle},
		}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}

	var sawIdle bool
	var gotErr error
	deadline := time.After(3 * time.Second)
waitLoop:
	for {
		select {
		case ev := <-e.Events():
			switch ev.Kind {
			case EventIdle:
				sawIdle = true
			case EventError:
				gotErr = ev.Err
				break waitLoop
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventError")
		}
	}
	if sawIdle {
		t.Fatal("a fatal (worktree-gone) checkpoint must not also emit EventIdle for the same turn")
	}
	if gotErr == nil {
		t.Fatal("EventError carried a nil error")
	}

	waitState(t, e, f.ID, StatePaused)
	snap := e.Get(f.ID).Snapshot()
	if snap.Err == nil {
		t.Error("session ended with no recorded error")
	}
	if !containsSubstring(snap.Activity, "checkpoint commit failed") {
		t.Errorf("activity missing checkpoint failure line: %v", snap.Activity)
	}

	stored, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != domain.StageImplement {
		t.Errorf("stored stage = %s, want StageImplement (must not advance with the worktree gone)", stored.Stage)
	}
}

func containsSubstring(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
