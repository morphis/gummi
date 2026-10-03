package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestABrokenBaseDoesNotExcuseACardForGood is the regression for a real
// card cut from main before another card landed the package it builds on.
// Its approval baseline failed build on that base, so build was excused —
// "already failing on the fresh branch; not gated at verify" — and after
// the other card landed and this one was rebased, nothing measured it
// again: a card that then broke the build itself would have verified.
//
// The baseline now records the commit it was measured on, and verify
// re-measures an excusal whose base the card no longer forks from.
func TestABrokenBaseDoesNotExcuseACardForGood(t *testing.T) {
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var kickoff string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleReviewer {
			mu.Lock()
			kickoff = msg
			mu.Unlock()
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "VERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()

	f := feature(2, "count chars", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	tree := filepath.Join(wt.Root(), f.WorktreePath())
	writeSpecChecks(t, wt, f, "- name: build\n  cmd: test -f textstat.go\n")

	// 1. approval: the base has no package yet, so build fails there and
	// is excused — measured on the fork point, and saying so
	fork0 := e.forkPointOf(ctx, f)
	if _, err := e.BaselineChecks(ctx, f); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.CheckBaseline(ctx, f.ID)
	if got := state.ExcusedChecks(rows); len(got) != 1 || got[0] != "build" {
		t.Fatalf("excused = %v, want build (the base has no package)", got)
	}
	if on := state.ExcusedOn(rows); on == "" || on != fork0 {
		t.Fatalf("excusal measured on %q, want the fork point %q", on, fork0)
	}

	// 2. another card lands the package on main, and this card is rebased
	// onto it — its base moves
	main := wt.RepoRoot()
	if err := os.WriteFile(filepath.Join(main, "textstat.go"), []byte("package textstat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, main, "add", "textstat.go")
	git(t, main, "commit", "-q", "-m", "textstat: the package")
	if err := wt.RebaseOnMain(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if err := wt.ReanchorOnMain(ctx, &f); err != nil {
		t.Fatal(err)
	}

	// 3. and then this card breaks the build itself
	git(t, tree, "rm", "-q", "textstat.go")
	git(t, tree, "commit", "-q", "-m", "oops")

	f, err := store.Transition(ctx, f.ID, domain.StageVerify, state.ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitActivity(t, e, f.ID, "worktree committed", "checkpoint commit failed", "nothing to commit")

	mu.Lock()
	got := kickoff
	mu.Unlock()
	if strings.Contains(got, "pre-existing") || !strings.Contains(got, "build: FAIL (exit 1)") {
		t.Errorf("verify still wrote the card's own breakage off as pre-existing:\n%s", got)
	}
	snap := e.Get(f.ID).Snapshot()
	if snap.VerdictFloor != "blocked" {
		t.Errorf("verdict floor = %q, want blocked on the card's own build failure", snap.VerdictFloor)
	}
	if acts := strings.Join(snap.Activity, "\n"); !strings.Contains(acts, "re-measured the excused checks") || !strings.Contains(acts, "build pass there") {
		t.Errorf("the re-measure is not on the thread:\n%s", acts)
	}
	rows, _ = store.CheckBaseline(ctx, f.ID)
	if len(state.ExcusedChecks(rows)) != 0 {
		t.Errorf("build is still excused after the base that broke it moved: %+v", rows)
	}
	if rows[0].BaseRev == fork0 || rows[0].BaseRev == "" {
		t.Errorf("the new baseline names %q, want the new fork point", rows[0].BaseRev)
	}
}

// A baseline whose base has not moved is not re-measured: the check is
// the repo's test suite, and running it twice for nothing is minutes.
func TestAnUnmovedBaseIsNotRemeasured(t *testing.T) {
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(agent.NewFake("ok")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := feature(1, "x", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	writeSpecChecks(t, wt, f, "- name: build\n  cmd: \"false\"\n")
	if _, err := e.BaselineChecks(ctx, f); err != nil {
		t.Fatal(err)
	}
	res, err := e.RebaselineIfBaseMoved(ctx, f)
	if err != nil || res.Moved {
		t.Fatalf("RebaselineIfBaseMoved = %+v, %v; want no re-measure on an unmoved base", res, err)
	}
}
