package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

func cliGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// verifiedCLIRepo builds a temp repo with a feature worktree holding one
// committed file, the card parked at StageVerify with VerifiedAt set, so the
// merge/clean commands have a real verified branch to act on. It chdirs the
// test process into the repo root (where the commands expect to run).
func verifiedCLIRepo(t *testing.T) (*state.Store, domain.Feature) {
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

	ws, err := state.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, store)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	id, _ := domain.NewFeatureID(9)
	slug, _ := domain.Slugify("JSON export")
	f := domain.Feature{
		ID: id, Num: 9, Kind: domain.KindFeature, Title: "JSON export", Slug: slug,
		Stage: domain.StageVerify, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerifiedAt(context.Background(), id, now); err != nil {
		t.Fatal(err)
	}
	p, err := wt.Create(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "feature.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, p, "add", ".")
	cliGit(t, p, "commit", "-q", "-m", "feature work")
	return store, f
}

// A verified card merges and exits 0: main advances to a commit carrying
// the caller's message and the card moves to done.
func TestMergeCommandLandsVerifiedBranch(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	before := cliGit(t, ".", "rev-parse", "HEAD")

	if err := runMerge([]string{string(f.ID), "-m", "feat(export): land headlessly"}); err != nil {
		t.Fatalf("runMerge: %v", err)
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageDone {
		t.Fatalf("stage = %s, want done", got.Stage)
	}
	head := cliGit(t, ".", "rev-parse", "HEAD")
	if head == before {
		t.Fatal("main did not advance on merge")
	}
	if msg := cliGit(t, ".", "log", "-1", "--format=%s"); msg != "feat(export): land headlessly" {
		t.Fatalf("landed subject = %q", msg)
	}
}

// The cobra layer accepts the documented -m shorthand (not just --message)
// end-to-end: a verified card merges and exits 0 when invoked as `gummi merge
// <id> -m <msg>` through rootCmd. This guards against a regression where the
// flag is registered as --m (no shorthand) and a single-dash -m is rejected
// before runMerge ever runs.
func TestMergeCobraShorthandFlag(t *testing.T) {
	_, f := verifiedCLIRepo(t)
	rootCmd.SetArgs([]string{"merge", string(f.ID), "-m", "feat(export): land headlessly"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute(merge -m): %v", err)
	}
	if msg := cliGit(t, ".", "log", "-1", "--format=%s"); msg != "feat(export): land headlessly" {
		t.Fatalf("landed subject = %q", msg)
	}
}

// A missing -m fails before touching git (no workspace, no repo needed).
func TestMergeCommandRequiresMessage(t *testing.T) {
	if err := runMerge([]string{"FD-009"}); err == nil {
		t.Fatal("merge without -m accepted")
	}
}

// A landed card cleans and exits 0: the worktree and branch are removed.
func TestCleanCommandRemovesLanded(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	if err := runMerge([]string{string(f.ID), "-m", "feat(export): land headlessly"}); err != nil {
		t.Fatalf("runMerge: %v", err)
	}
	if err := runClean([]string{string(f.ID)}); err != nil {
		t.Fatalf("runClean: %v", err)
	}
	wt, err := worktree.NewManager(context.Background(), ".", store)
	if err != nil {
		t.Fatal(err)
	}
	if ex, _ := wt.Exists(context.Background(), &f); ex {
		t.Error("worktree still present after clean")
	}
	if ok, _ := wt.BranchExists(context.Background(), &f); ok {
		t.Error("branch still present after clean")
	}
}
