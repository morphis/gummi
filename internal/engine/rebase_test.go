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

func TestRebaseHintsAndTools(t *testing.T) {
	f := feature(1, "Dark mode", domain.StageVerify)

	joined := strings.Join(stageHints(f, "spec.md", flavorRebase), "\n")
	if !strings.Contains(joined, "Task: Rebase onto main") {
		t.Error("rebase hints missing the rebase contract")
	}
	if !strings.Contains(joined, "You are the implementer") {
		t.Error("rebase contract not issued for the implementer role")
	}
	if plain := strings.Join(stageHints(f, "spec.md", flavorStage), "\n"); strings.Contains(plain, "Task: Rebase") {
		t.Error("stage hints leaked the rebase contract")
	}

	if stageTools(domain.StageVerify, flavorRebase) != nil {
		t.Error("rebase session unexpectedly offered client tools")
	}
	if toolHint(domain.StageVerify, flavorRebase) != "" {
		t.Error("rebase session got a tool hint with no tools")
	}
}

func TestRunRebaseRunsAsImplementer(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(1, "Dark mode", domain.StageVerify)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	var got agent.SessionOpts
	var kicked string
	ag := &agent.Fake{
		Caps: agent.Capabilities{ClientTools: true},
		Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			got, kicked = opts, msg
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Rebased cleanly."},
				{Kind: agent.EventIdle},
			}
		},
	}
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	if err := e.RunRebase(ctx, f, []string{"a.go", "b.go"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)

	s := e.Get(f.ID)
	if s == nil || !s.Snapshot().Rebase {
		t.Fatal("session not marked as rebase")
	}
	if got.Role != agent.RoleImplementer {
		t.Errorf("rebase spawned as %s, want implementer", got.Role)
	}
	if !strings.Contains(strings.Join(got.SystemHints, "\n"), "Task: Rebase onto main") {
		t.Error("rebase session missing its contract")
	}
	if len(got.Tools) != 0 {
		t.Errorf("rebase session offered tools: %v", got.Tools)
	}
	head, err := wt.MainHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kicked, "git rebase "+head) {
		t.Errorf("kickoff missing the rebase target:\n%s", kicked)
	}
	if !strings.Contains(kicked, "a.go, b.go") {
		t.Errorf("kickoff missing the conflicted files:\n%s", kicked)
	}
}

func TestSettleAbortsLeftoverRebase(t *testing.T) {
	ws, store, wt := newRepo(t)
	f := feature(2, "Conflict", domain.StageVerify)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	// conflicting edits on both sides, then a rebase left mid-flight —
	// what a dead or confused rebase session would strand.
	root := wt.Root()
	p := filepath.Join(root, f.WorktreePath())
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(p, "README.md"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(p, "add", ".")
	git(p, "commit", "-qm", "feature edit")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".")
	git(root, "commit", "-qm", "main edit")
	head, err := wt.MainHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), "git", "-C", p, "rebase", head).CombinedOutput(); err == nil {
		t.Fatalf("conflicting rebase did not stop:\n%s", out)
	}

	e := New(Config{Agents: singleAgent(agent.NewFake("x")), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	s := &Session{Feature: f, Role: agent.RoleImplementer, Rebase: true, done: make(chan struct{})}
	e.settle(s)

	if in, err := wt.RebaseInProgress(context.Background(), &f); in || err != nil {
		t.Errorf("settle left the worktree mid-rebase: %v %v", in, err)
	}
	if dirty, err := wt.Dirty(context.Background(), &f); dirty || err != nil {
		t.Errorf("settle left the worktree dirty: %v %v", dirty, err)
	}
	if joined := strings.Join(s.Snapshot().Activity, "\n"); !strings.Contains(joined, "aborted") {
		t.Errorf("no abort note in activity: %q", joined)
	}
}

func TestRestoreRecoversRebaseFlag(t *testing.T) {
	ws, store, wt := newRepo(t)
	ctx := context.Background()
	f := feature(3, "Dark mode", domain.StageVerify)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	e1 := persistEngine(t, agent.NewFake("Rebased."), ws, store, wt)
	if err := e1.RunRebase(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	waitState(t, e1, f.ID, StateDone)
	e1.Close()

	e2 := persistEngine(t, agent.NewFake("x"), ws, store, wt)
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	s := e2.Get(f.ID)
	if s == nil {
		t.Fatal("rebase session not restored")
	}
	snap := s.Snapshot()
	if !snap.Rebase || snap.Role != agent.RoleImplementer {
		t.Errorf("restored rebase = %v role = %s, want rebase implementer", snap.Rebase, snap.Role)
	}
}
