package worktree

import (
	"strings"
	"testing"
)

func TestCommitAll(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(7, "Checkpoint me")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}

	// a mix of a tracked edit and a brand-new file
	writeFile(t, p, "README.md", "edited\n")
	writeFile(t, p, "pkg/new.go", "package pkg\n")
	committed, err := m.CommitAll(ctx, f, string(f.ID)+": implement checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("CommitAll on a dirty worktree reported no commit")
	}
	if out := mustGit(t, p, "status", "--porcelain"); out != "" {
		t.Errorf("worktree not clean after CommitAll:\n%s", out)
	}
	if got := mustGit(t, p, "log", "-1", "--format=%s"); strings.TrimSpace(got) != "FD-007: implement checkpoint" {
		t.Errorf("commit subject = %q", got)
	}
	if got := mustGit(t, p, "show", "--stat", "--format=", "HEAD"); !strings.Contains(got, "pkg/new.go") {
		t.Errorf("new file missing from checkpoint commit:\n%s", got)
	}

	// clean worktree: no commit, no error
	head := mustGit(t, p, "rev-parse", "HEAD")
	if committed, err := m.CommitAll(ctx, f, "noop"); committed || err != nil {
		t.Errorf("CommitAll on clean worktree = %v, %v; want false, nil", committed, err)
	}
	if got := mustGit(t, p, "rev-parse", "HEAD"); got != head {
		t.Error("CommitAll on clean worktree moved HEAD")
	}
}

func TestCommitAllEmptyMessageRefused(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(7, "Checkpoint me")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "x.txt", "x\n")
	for _, msg := range []string{"", " \n\t"} {
		if _, err := m.CommitAll(ctx, f, msg); err == nil {
			t.Errorf("empty message %q accepted", msg)
		}
	}
}

func TestCommitAllNoWorktree(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(7, "Checkpoint me")
	if _, err := m.CommitAll(ctx, f, "msg"); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Fatalf("err = %v, want a no-worktree refusal", err)
	}
}
