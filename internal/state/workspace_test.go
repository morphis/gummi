package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// nestedWorkspace builds a parent workspace root with a git repo in a
// nested subdirectory — .gummi at ws, .git at ws/git/lxd.
func nestedWorkspace(t *testing.T) (ws, repo string) {
	t.Helper()
	ws = t.TempDir()
	repo = filepath.Join(ws, "git", "lxd")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	return ws, repo
}

func TestInitNestedRoots(t *testing.T) {
	ws, repo := nestedWorkspace(t)
	w, err := Init(ws, repo)
	if err != nil {
		t.Fatalf("Init(ws, repo) nested: %v", err)
	}
	if w.Root != ws {
		t.Errorf("Root = %q, want ws %q", w.Root, ws)
	}
	if w.RepoRoot != repo {
		t.Errorf("RepoRoot = %q, want repo %q", w.RepoRoot, repo)
	}
	// the skeleton is created at ws, not the repo
	if fi, err := os.Stat(w.GummiDir()); err != nil || !fi.IsDir() {
		t.Errorf(".gummi at ws not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gummi")); !os.IsNotExist(err) {
		t.Errorf("skeleton materialized in the nested repo: %v", err)
	}
}

func TestInitRequiresRepoGit(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "git", "lxd")
	if err := os.MkdirAll(repo, 0o750); err != nil { // no .git
		t.Fatal(err)
	}
	if _, err := Init(ws, repo); err == nil {
		t.Fatal("Init with a repo lacking .git should fail")
	}
}

func TestOpenRecordsBothRoots(t *testing.T) {
	ws, repo := nestedWorkspace(t)
	if _, err := Init(ws, repo); err != nil {
		t.Fatal(err)
	}
	w, err := Open(ws, repo)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if w.Root != ws || w.RepoRoot != repo {
		t.Errorf("Open roots = (%q, %q), want (%q, %q)", w.Root, w.RepoRoot, ws, repo)
	}
}

func TestInitNestedInsideWorktreeStillRefuses(t *testing.T) {
	p := nestedLayout(t) // a parent workspace with a managed worktree FD-042
	ws := filepath.Join(p, ".gummi", "worktrees", "FD-042")
	if _, err := Init(ws, ws); !errors.Is(err, ErrNestedInit) {
		t.Fatalf("Init inside a managed worktree = %v, want ErrNestedInit", err)
	}
}
