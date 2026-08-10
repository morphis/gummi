package worktree

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestMainDirtyPathsMixedstate exercises MainDirtyPaths against a repo
// holding every relevant working-tree shape: a clean tracked file, a
// staged-modified tracked file, an unstaged-modified tracked file, a new
// untracked file, a .gitignore-covered path, a staged rename, and a
// .gummi scratch file. It returns exactly the sorted set of dirty paths —
// .gummi excluded, ignored paths absent, the rename's destination but not
// its origin — and leaves MainTrackedDirty's behavior untouched.
func TestMainDirtyPathsMixedstate(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)

	writeFile(t, root, "tracked_clean.go", "package main\n")
	writeFile(t, root, "tracked_staged.go", "package main\n")
	writeFile(t, root, "tracked_dirty.go", "package main\n")
	writeFile(t, root, ".gitignore", "*.tmp\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "seed")

	// per-shape dirt after the seed commit
	mustGit(t, root, "mv", "tracked_clean.go", "tracked_renamed.go") // staged rename
	writeFile(t, root, "tracked_staged.go", "package main\n\n// staged\n")
	mustGit(t, root, "add", "tracked_staged.go")
	writeFile(t, root, "tracked_dirty.go", "package main\n\n// dirty\n")
	writeFile(t, root, "untracked_new.go", "package main\n")                // new untracked
	writeFile(t, root, "ignored.tmp", "x\n")                                // matches .gitignore *.tmp
	writeFile(t, root, filepath.Join(".gummi", "scratch.txt"), "scratch\n") // .gummi excluded

	paths, err := m.MainDirtyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tracked_dirty.go",
		"tracked_renamed.go",
		"tracked_staged.go",
		"untracked_new.go",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("MainDirtyPaths = %v, want %v", paths, want)
	}

	// The sibling stays independent: MainTrackedDirty ignores untracked
	// files and sees tracked changes.
	dirty, err := m.MainTrackedDirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("MainTrackedDirty = false, want true (tracked changes present)")
	}
}

// TestMainDirtyPathsRenameDestination pins the -z porcelain rename edge:
// a staged rename yields only the destination path, never the origin. The
// naive record-per-NUL split would emit the origin as a spurious dirty
// entry.
func TestMainDirtyPathsRenameDestination(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	writeFile(t, root, "old.go", "package main\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "seed")

	mustGit(t, root, "mv", "old.go", "new.go")

	paths, err := m.MainDirtyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("MainDirtyPaths = %v, want %v", paths, want)
	}
}

// TestMainDirtyPathsClean returns an empty set on a clean tree.
func TestMainDirtyPathsClean(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	paths, err := m.MainDirtyPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("MainDirtyPaths on clean tree = %v, want []", paths)
	}
}
