package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// TestDesignStageRunsInTheCardsWorktree: a design stage's session gets
// the card's OWN branch worktree as its working directory — the same
// directory implement will use, kept for the card's whole life. Every
// backend cages its file tools to opts.WorkDir, so this is where "do not
// write to the operator's repo" stops being a request and becomes a
// boundary; and because the tree is the card's real one, nothing has to
// be handed off later.
func TestDesignStageRunsInTheCardsWorktree(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "Dark mode", domain.StageSpec)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatalf("attach spec: %v", err)
	}

	got := rec.opts()
	want, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkDir != want {
		t.Fatalf("spec workdir = %s, want the card's worktree %s", got.WorkDir, want)
	}
	if got.WorkDir == wt.RepoRoot() {
		t.Fatal("spec stage still runs in the main checkout")
	}
	if _, err := os.Stat(got.WorkDir); err != nil {
		t.Fatalf("worktree not materialized: %v", err)
	}
	// the design artifact stays at its workspace home, outside every
	// working directory — that contract is unchanged
	if !strings.HasPrefix(got.ArtifactPath, filepath.Join(ws.Root, ".gummi")) {
		t.Fatalf("artifact path = %s, want it under the workspace .gummi", got.ArtifactPath)
	}
	// the branch worktree is cut BY the design stage, not at approval
	if ok, err := wt.Exists(context.Background(), &f); err != nil || !ok {
		t.Fatal("a design stage did not materialize the card's branch worktree")
	}
}

// TestDesignStageWritesDoNotTripMain is the observed regression this fix
// exists for: an agent exploring at spec ran ordinary Go commands, the
// toolchain rewrote go.sum in its working directory, and the tripwire
// escalated ("the agent dirtied the main checkout ... go.sum") — parking
// the card having produced nothing, and killing a benchmark run. Writing
// in its own working directory must now be unremarkable.
func TestDesignStageWritesDoNotTripMain(t *testing.T) {
	ws, store, wt := newRepo(t)
	ag := agent.NewFake("ack")
	ag.Responder = func(opts agent.SessionOpts, _ string) []agent.Event {
		// exactly what the toolchain did: rewrite tracked files in cwd
		writeAt(t, opts.WorkDir, "go.sum", "h1:rewritten\n")
		writeAt(t, opts.WorkDir, "README.md", "explored\n")
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "Dark mode", domain.StageSpec)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	// a trip would emit EventTripwire instead and hang this wait
	waitFor(t, e, EventIdle)

	paths, err := wt.MainDirtyPaths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("main checkout dirty after a design stage: %v", paths)
	}
	body, err := os.ReadFile(filepath.Join(wt.RepoRoot(), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "explored\n" {
		t.Fatal("the design stage's write landed in the main checkout")
	}
}

// TestDesignStageEditsSurviveTheApprovalGate is the inversion this phase
// is for. A design stage's edits used to be discarded at the approval
// gate — a deliberate hand-off between a throwaway tree and the card's
// real one. There is only one tree now, so a spike written while planning
// is simply early work on the branch implement continues, and crossing
// the gate moves nothing and deletes nothing.
func TestDesignStageEditsSurviveTheApprovalGate(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "Dark mode", domain.StageSpec)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Attach(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	tree := rec.opts().WorkDir
	writeAt(t, tree, "spike.go", "package main\n")
	// the spec stage has to have written its section, or the
	// undrafted-sections gate holds the approval shut before this test's
	// own question is reached
	fillPromotedSection(t, wt, f, "Chosen approach", "\nA settings toggle.\n\n")

	res := mustAdvance(t, e, f.ID)
	if !res.EnteredWorktree {
		t.Fatalf("spec approval did not report the work crossing (to=%s status=%d)", res.To, res.Status)
	}

	wtPath, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if tree != wtPath {
		t.Fatalf("the design stage ran in %s but the card's worktree is %s — there should be only one tree", tree, wtPath)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "spike.go")); err != nil {
		t.Fatalf("the spike written while planning did not survive the gate: %v", err)
	}
}
