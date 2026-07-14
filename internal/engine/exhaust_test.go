package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestExhaustWithCommittedWorkReadsAsReady: a stage whose agent committed
// its work before the cap tipped over parks as "work committed", and the
// event flags it, so the UI can present advance-or-top-up rather than
// implying lost work.
func TestExhaustWithCommittedWorkReadsAsReady(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)
	wtDir := filepath.Join(wt.Root(), f.WorktreePath())

	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		// the agent produces and commits its deliverable, then a usage
		// event tips over the cap at wrap-up
		if err := os.WriteFile(filepath.Join(wtDir, "impl.go"), []byte("package x\n"), 0o600); err == nil {
			run := func(a ...string) {
				_ = exec.CommandContext(context.Background(), "git", append([]string{"-C", wtDir}, a...)...).Run()
			}
			run("add", "-A")
			run("commit", "-m", "implement the feature")
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "done"},
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 120}},
			{Kind: agent.EventBudgetExhausted, Usage: agent.Usage{Credits: 120}},
		}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100, Persist: true})
	t.Cleanup(func() { e.Close() })

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, e, EventExhausted)
	if !ev.Committed {
		t.Fatal("exhaustion with committed work did not flag Committed")
	}
	waitActivity(t, e, "FD-001", "work committed")
}

// TestExhaustMidEditKeepsStoppedWording: nothing committed, dirty tree —
// the park keeps its cautious "stopped for review" wording and does not
// claim the work is safe.
func TestExhaustMidEditKeepsStoppedWording(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "impl", domain.StageImplement)
	withWorktree(t, wt, f)

	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 120}},
			{Kind: agent.EventBudgetExhausted, Usage: agent.Usage{Credits: 120}},
		}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, StageBudget: 100, Persist: true})
	t.Cleanup(func() { e.Close() })

	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, e, EventExhausted)
	if ev.Committed {
		t.Fatal("a stage with no committed work claimed work committed")
	}
	waitActivity(t, e, "FD-001", "budget exhausted")
}

// TestSessionErrorPersistsForReconstruction: a failed run's error text is
// persisted, so a restart can rebuild its needs-attention failure item.
func TestSessionErrorPersistsForReconstruction(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: errBoom}}
	}}
	e := New(Config{Agent: ag, Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1, Persist: true})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "impl", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventError)

	snaps, err := store.LoadSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || !strings.Contains(snaps[0].Error, "boom") {
		t.Fatalf("session error not persisted: %+v", snaps)
	}

	// a fresh engine restores the session with its error intact
	e2 := New(Config{Agent: agent.NewFake("ok"), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs := e2.Get("FD-001")
	if rs == nil || rs.Snapshot().Err == nil || !strings.Contains(rs.Snapshot().Err.Error(), "boom") {
		t.Fatalf("restored session lost its error: %+v", rs)
	}
}

var errBoom = fakeEngineErr("provider boom")

type fakeEngineErr string

func (e fakeEngineErr) Error() string { return string(e) }
