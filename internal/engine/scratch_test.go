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

// TestDesignStageRunsInScratchTree: an interactive design stage's session
// gets the card's scratch tree as its working directory, not the main
// checkout. Every backend cages its file tools to opts.WorkDir, so this
// is where "do not write to the operator's repo" stops being a request
// and becomes a boundary.
func TestDesignStageRunsInScratchTree(t *testing.T) {
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
	want := scratchPath(t, wt, &f)
	if got.WorkDir != want {
		t.Fatalf("spec workdir = %s, want scratch tree %s", got.WorkDir, want)
	}
	if got.WorkDir == wt.RepoRoot() {
		t.Fatal("spec stage still runs in the main checkout")
	}
	if _, err := os.Stat(got.WorkDir); err != nil {
		t.Fatalf("scratch tree not materialized: %v", err)
	}
	// the design artifact stays at its workspace home, outside every
	// working directory — that contract is unchanged
	if !strings.HasPrefix(got.ArtifactPath, filepath.Join(ws.Root, ".gummi")) {
		t.Fatalf("artifact path = %s, want it under the workspace .gummi", got.ArtifactPath)
	}
	// and no branch worktree was cut for a card still in design
	if ok, _ := wt.Exists(context.Background(), &f); ok {
		t.Fatal("a design stage materialized the card's branch worktree")
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

// TestScratchDiscardedAtWorktreeCut: the scratch tree's edits must not
// silently become the card's work. The approval gate promotes the
// artifact and drops the tree — that hand-off is the decision, not an
// emergent leftover.
func TestScratchDiscardedAtWorktreeCut(t *testing.T) {
	ws, store, wt := newRepo(t)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "Dark mode", domain.StageSpec)
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	scratch, err := wt.EnsureScratch(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	writeAt(t, scratch, "half-done.go", "package main\n")

	res := mustAdvance(t, e, f.ID)
	if !res.EnteredWorktree {
		t.Fatalf("spec approval did not cut the worktree (to=%s status=%d)", res.To, res.Status)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch tree survived the hand-off: %v", err)
	}
	// the card's real worktree carries none of it
	wtPath, err := wt.Path(&f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "half-done.go")); !os.IsNotExist(err) {
		t.Fatal("a design stage's stray edit arrived on the branch as work")
	}
}
