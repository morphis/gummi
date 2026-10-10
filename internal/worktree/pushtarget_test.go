package worktree

import (
	"context"
	"errors"
	"os/exec"
	"testing"
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
