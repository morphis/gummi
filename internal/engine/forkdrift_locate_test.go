package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// A research investigate stage resolves to the main checkout with the
// artifact at its workspace home (.gummi/research), never materializing a
// worktree — research branches receive no commits, so there is nothing to
// isolate in one.
func TestLocateResearchUsesRepoRoot(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "locate rs", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	workDir, specPath, err := e.locate(context.Background(), f)
	if err != nil {
		t.Fatalf("locate failed: %v", err)
	}
	if workDir != wt.RepoRoot() {
		t.Fatalf("workdir = %s, want repo root %s", workDir, wt.RepoRoot())
	}
	want := filepath.Join(wt.RepoRoot(), f.ArtifactPath())
	if specPath != want {
		t.Fatalf("spec path = %s, want %s", specPath, want)
	}
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("artifact not promoted to %s: %v", want, statErr)
	}
	if ok, _ := wt.Exists(context.Background(), &f); ok {
		t.Fatal("research locate materialized a worktree")
	}
}

// TestRunResearchInvestigateSpawnsArchitectNoWorktree: the research
// work stage must actually run — roleForStage must resolve investigate to
// an architect session (previously it returned no role, so Run errored
// "stage investigate has no agent action") and the session must run in
// the main checkout with no worktree materialized, advancing to the
// gated shape stage.
func TestRunResearchInvestigateSpawnsArchitectNoWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "run rs investigate", domain.StageInvestigate)
	f.ID = domain.FeatureID("RS-001")
	f.Kind = domain.KindResearch
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f); err != nil {
		t.Fatalf("run research investigate: %v", err)
	}
	got := rec.opts()
	if got.Role != agent.RoleArchitect {
		t.Errorf("investigate session role = %s, want architect", got.Role)
	}
	if got.WorkDir != wt.RepoRoot() {
		t.Errorf("investigate workdir = %s, want repo root %s", got.WorkDir, wt.RepoRoot())
	}
	if ok, _ := wt.Exists(context.Background(), &f); ok {
		t.Fatal("running research investigate materialized a worktree")
	}
}
