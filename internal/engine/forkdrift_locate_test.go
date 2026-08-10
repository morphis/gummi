package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// The resume/entry hop refuses a feature worktree after main was rewound:
// Run synchronously drives schedule → startAutonomous → locate, where the
// guard fires; the error is captured on the session (StatePaused) and the
// stored stage never moves off the pre-entry stage. The session stays in
// e.live[] because freeSlot only releases the running slot, so the paused
// shape (not nil) is what Run leaves behind.
func TestLocateRefusesOnDriftForFeatureWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "locate drift", domain.StageImplement)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, f)

	rewindMain(t, ws.Root)

	if err := e.Run(f); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	s := e.Get(f.ID)
	if s == nil {
		t.Fatal("no live session recorded for the feature")
	}
	if s.State() != StatePaused {
		t.Fatalf("session state = %s, want StatePaused", s.State())
	}
	snap := s.Snapshot()
	var fe *worktree.ForkDriftError
	if !errors.As(snap.Err, &fe) {
		t.Fatalf("session error not a *worktree.ForkDriftError: %T: %v", snap.Err, snap.Err)
	}

	stored, gerr := store.GetFeature(context.Background(), f.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if stored.Stage != domain.StageImplement {
		t.Errorf("stored stage = %s, want StageImplement (no stage move)", stored.Stage)
	}
}

// Interactive stages resolve to the main checkout before the worktree
// guard is reached, so a rewind must not refuse an interactive attach.
func TestLocateInteractiveStageIgnoresDrift(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "interactive ignore", domain.StageBrainstorm)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	rewindMain(t, ws.Root)

	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatalf("Attach refused interactive stage on rewound main: %v", err)
	}
}
