package worktree

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestPushTargetChoosesTheRemoteGitWould(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	target := func(remotes ...string) (PushTarget, error) {
		return pushTargetIn(ctx, repo, "feat/x", "main", remotes)
	}

	if _, err := target(); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("no remote = %v", err)
	}
	if _, err := target("fork", "upstream"); !errors.Is(err, ErrRemoteAmbiguous) {
		t.Fatalf("two remotes, neither origin = %v", err)
	}
	if got, _ := target("fork"); got.Remote != "fork" || got.Branch != "feat/x" {
		t.Fatalf("the sole remote = %+v", got)
	}
	if got, _ := target("origin", "upstream"); got.Remote != "origin" || got.How != "origin" {
		t.Fatalf("origin among several = %+v", got)
	}

	// a branch that tracks the base still pushes as itself, never onto it
	git("config", "branch.feat/x.remote", "upstream")
	git("config", "branch.feat/x.merge", "refs/heads/main")
	if got, _ := target("origin", "upstream"); got.Remote != "upstream" || got.Branch != "feat/x" || got.How != "upstream" {
		t.Fatalf("tracking the base = %+v", got)
	}
	// an upstream under another name on the push remote is where it lives
	git("config", "branch.feat/x.merge", "refs/heads/their-name")
	if got, _ := target("origin", "upstream"); got.Branch != "their-name" {
		t.Fatalf("an upstream of another name = %+v", got)
	}
	// the triangular settings win, and the upstream's name does not
	// follow the branch to another remote
	git("config", "remote.pushDefault", "origin")
	if got, _ := target("origin", "upstream"); got.Remote != "origin" || got.How != "pushDefault" || got.Branch != "feat/x" {
		t.Fatalf("remote.pushDefault = %+v", got)
	}
	git("config", "branch.feat/x.pushRemote", "fork")
	if got, _ := target("origin", "upstream", "fork"); got.Remote != "fork" || got.How != "pushRemote" {
		t.Fatalf("branch pushRemote = %+v", got)
	}
}

func TestAChosenPushRemoteIsGitsOwnSettingAndCanBePutBack(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", "git@github.com:acme/w.git")
	git("remote", "add", "fork", "git@github.com:me/w.git")
	m := &Manager{repo: repo}
	f := &domain.Feature{ID: "FD-001", Slug: "x"}
	const key = "branch.gummi/FD-001-x.pushRemote"

	if got := m.PushRemote(ctx, f); got != "" {
		t.Fatalf("pushRemote before any choice = %q", got)
	}
	if err := m.SetPushRemote(ctx, f, "elsewhere"); err == nil {
		t.Fatal("a remote the repository does not have was accepted")
	}
	if err := m.SetPushRemote(ctx, f, "fork"); err != nil {
		t.Fatal(err)
	}
	if got := git("config", "--get", key); got != "fork" || m.PushRemote(ctx, f) != "fork" {
		t.Fatalf("pushRemote = %q, want fork", got)
	}
	if tgt, err := pushTargetIn(ctx, repo, f.BranchName(), "main", m.Remotes(ctx)); err != nil || tgt.Remote != "fork" || tgt.TrackingRef() != "refs/remotes/fork/gummi/FD-001-x" {
		t.Fatalf("target after the choice = %+v %v", tgt, err)
	}
	// put back what was there: a value, or nothing at all
	m.RestorePushRemote(ctx, f, "origin")
	if got := m.PushRemote(ctx, f); got != "origin" {
		t.Fatalf("restored to %q, want origin", got)
	}
	m.RestorePushRemote(ctx, f, "")
	if got := m.PushRemote(ctx, f); got != "" {
		t.Fatalf("restored to %q, want it unset", got)
	}
}
