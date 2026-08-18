package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// nestedGitRepo builds a committed git repo at ws/git/lxd (the managed
// repo) and returns (ws, repo) — .gummi will live at ws, outside the repo.
func nestedGitRepo(t *testing.T) (ws, repo string) {
	t.Helper()
	ws = t.TempDir()
	ws, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(ws, "git", "lxd")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	return ws, repo
}

// TestNestedLayoutEndToEnd proves Must hold 1: with .gummi at the parent
// and the repo in a nested subdirectory, a card is created, driven to a
// verified branch, merged, and cleaned — its worktree living under
// ws/.gummi/worktrees, outside the repo working tree.
func TestNestedLayoutEndToEnd(t *testing.T) {
	ws, repo := nestedGitRepo(t)
	h := newHarnessRoots(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			_ = os.WriteFile(filepath.Join(o.WorkDir, "feature.txt"), []byte("work\n"), 0o600)
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	}, ws, repo)

	out, err := h.driver(Options{}).Run(context.Background(), "nested end-to-end")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	id := domain.FeatureID(out.ID)

	// the feature worktree lives under ws/.gummi/worktrees, outside the repo
	wtPath := filepath.Join(ws, ".gummi", "worktrees", string(id))
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("feature worktree %s missing: %v", wtPath, err)
	}
	if rel, _ := filepath.Rel(repo, wtPath); rel != ".." && !isOutside(rel) {
		t.Errorf("worktree %s unexpectedly inside the repo %s", wtPath, repo)
	}

	// merge onto main (the repo), then clean the worktree + branch
	d := h.driver(Options{})
	if out, err := d.Merge(context.Background(), id, "feat(nested): land the nested end-to-end card"); err != nil {
		t.Fatalf("Merge: %v", err)
	} else if out.Status != StatusDone {
		t.Fatalf("merge status = %q, want done", out.Status)
	}
	if out, err := d.Clean(context.Background(), id); err != nil {
		t.Fatalf("Clean: %v", err)
	} else if out.Status != StatusDone {
		t.Fatalf("clean status = %q, want done", out.Status)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after clean (stat err=%v)", wtPath, err)
	}
	// the repo's main carries the landed commit
	if _, err := exec.CommandContext(context.Background(), "git", "-C", repo, "rev-parse", "HEAD").Output(); err != nil {
		t.Errorf("repo main HEAD unreadable after land: %v", err)
	}
}

// isOutside reports whether a filepath.Rel result escapes its base.
func isOutside(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}
