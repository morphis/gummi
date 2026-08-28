package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// commitCLIRepo builds a temp repo with a feature worktree holding one
// uncommitted, tracked-dirty file (mirroring squashCLIRepo's shape, but
// dirty rather than checkpointed), the card parked at StageImplement — the
// setup behind every headless commit CLI test. It chdirs the test process
// into the repo root, matching every other headless-command test.
func commitCLIRepo(t *testing.T) (*state.Store, domain.Feature) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cliGit(t, root, "init", "-q", "-b", "main")
	cliGit(t, root, "config", "user.name", "t")
	cliGit(t, root, "config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-q", "-m", "init")
	cliGit(t, root, "update-ref", "refs/remotes/origin/main", cliGit(t, root, "rev-parse", "HEAD"))

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	id, _ := domain.NewFeatureID(9)
	slug, _ := domain.Slugify("JSON export")
	f := domain.Feature{
		ID: id, Num: 9, Kind: domain.KindFeature, Title: "JSON export", Slug: slug,
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	p, err := wt.Create(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "stray.txt"), []byte("stray\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return store, f
}

// A card with an uncommitted, tracked-dirty worktree commits and exits 0:
// the worktree is clean afterward and the new commit carries the caller's
// message.
func TestCommitCommand_Happy(t *testing.T) {
	_, f := commitCLIRepo(t)
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	before := cliGit(t, wtPath, "rev-parse", "HEAD")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runCommit([]string{string(f.ID), "-m", "fix(export): commit stray worktree changes"})
	})
	if runErr != nil {
		t.Fatalf("runCommit: %v", runErr)
	}
	if !strings.Contains(out, "committed") {
		t.Fatalf("stdout = %q, want a committed line", out)
	}
	after := cliGit(t, wtPath, "rev-parse", "HEAD")
	if after == before {
		t.Fatal("branch did not advance on commit")
	}
	if msg := cliGit(t, wtPath, "log", "-1", "--format=%s"); msg != "fix(export): commit stray worktree changes" {
		t.Fatalf("commit subject = %q", msg)
	}
	if status := cliGit(t, wtPath, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree not clean after commit:\n%s", status)
	}
}

// The cobra layer accepts the documented -m shorthand and routes through to
// runCommit end-to-end (guards against the flag being registered without a
// shorthand, the same regression TestMergeCobraShorthandFlag guards for
// merge).
func TestCommitCobraShorthandFlag(t *testing.T) {
	_, f := commitCLIRepo(t)
	rootCmd.SetArgs([]string{"commit", string(f.ID), "-m", "fix(export): commit stray worktree changes"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute(commit -m): %v", err)
	}
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	if msg := cliGit(t, wtPath, "log", "-1", "--format=%s"); msg != "fix(export): commit stray worktree changes" {
		t.Fatalf("commit subject = %q", msg)
	}
}

// -m - reads the message from stdin.
func TestCommitCommand_StdinMessage(t *testing.T) {
	_, f := commitCLIRepo(t)

	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("fix(export): from stdin"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	if err := runCommit([]string{string(f.ID), "-m", "-"}); err != nil {
		t.Fatalf("runCommit: %v", err)
	}
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	if msg := cliGit(t, wtPath, "log", "-1", "--format=%s"); msg != "fix(export): from stdin" {
		t.Fatalf("commit subject = %q", msg)
	}
}

// A missing -m fails before touching git.
func TestCommitCommand_RequiresMessage(t *testing.T) {
	if err := runCommit([]string{"FD-009"}); err == nil {
		t.Fatal("commit without -m accepted")
	}
}

// A clean worktree is a no-op: exit 0, "nothing to commit" on stdout, branch
// tip unchanged.
func TestCommitCommand_NoopExitsZero(t *testing.T) {
	_, f := commitCLIRepo(t)
	wtPath := filepath.Join(".gummi", "worktrees", string(f.ID))
	// clean the worktree commitCLIRepo left dirty before exercising the no-op path.
	cliGit(t, wtPath, "add", ".")
	cliGit(t, wtPath, "commit", "-q", "-m", "checkpoint")
	before := cliGit(t, wtPath, "rev-parse", "HEAD")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runCommit([]string{string(f.ID), "-m", "fix(export): commit stray worktree changes"})
	})
	if runErr != nil {
		t.Fatalf("runCommit: %v", runErr)
	}
	if !strings.Contains(out, "nothing to commit") {
		t.Fatalf("stdout = %q, want a nothing-to-commit line", out)
	}
	if after := cliGit(t, wtPath, "rev-parse", "HEAD"); after != before {
		t.Fatalf("branch tip moved on a no-op commit: %s -> %s", before, after)
	}
}

// TestCommitThenSquashComposesOnPRLinkedCard proves the composition this
// feature exists for: squash alone refuses a PR-linked card's dirty
// worktree, but commit followed by squash both succeed and the card's
// PullRequest ref is unchanged before and after.
func TestCommitThenSquashComposesOnPRLinkedCard(t *testing.T) {
	store, f := commitCLIRepo(t)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 3, URL: "https://github.com/o/r/pull/3", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := store.SetPullRequest(context.Background(), f.ID, ref); err != nil {
		t.Fatal(err)
	}

	var squashErr error
	preOut := captureStdout(t, func() {
		squashErr = runSquash([]string{string(f.ID), "-m", "feat(export): collapsed"})
	})
	if squashErr == nil {
		t.Fatal("squash accepted a PR-linked card's dirty worktree")
	}
	if !strings.Contains(preOut, "uncommitted changes") {
		t.Fatalf("stdout = %q, want the dirty-worktree refusal (ErrDirtyWorktree) in the error event", preOut)
	}

	if err := runCommit([]string{string(f.ID), "-m", "fix(export): commit stray worktree changes"}); err != nil {
		t.Fatalf("runCommit: %v", err)
	}
	if err := runSquash([]string{string(f.ID), "-m", "feat(export): collapsed"}); err != nil {
		t.Fatalf("runSquash after commit: %v", err)
	}

	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest != ref {
		t.Fatalf("PullRequest = %+v, want unchanged %+v", got.PullRequest, ref)
	}
}
