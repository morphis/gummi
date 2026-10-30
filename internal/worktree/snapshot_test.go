package worktree

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// untouched is what a snapshot must leave as it was: the index file's
// bytes and time, HEAD, and what git status says. Status runs without
// optional locks, or it would refresh the index itself.
type untouched struct {
	index  []byte
	mtime  int64
	head   string
	status string
}

func observe(t *testing.T, dir string) untouched {
	t.Helper()
	var u untouched
	idx := mustGit(t, dir, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(idx) {
		idx = filepath.Join(dir, idx)
	}
	if fi, err := os.Stat(idx); err == nil {
		u.mtime = fi.ModTime().UnixNano()
		u.index, _ = os.ReadFile(idx)
	}
	u.head, _ = runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	u.status = mustGit(t, dir, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all")
	return u
}

func (u untouched) same(t *testing.T, dir string) {
	t.Helper()
	now := observe(t, dir)
	if !bytes.Equal(u.index, now.index) || u.mtime != now.mtime {
		t.Error("the snapshot touched the real index")
	}
	if u.head != now.head {
		t.Errorf("HEAD moved: %s → %s", u.head, now.head)
	}
	if u.status != now.status {
		t.Errorf("status changed:\n%s\n→\n%s", u.status, now.status)
	}
}

// treeFiles lists a tree's files and their contents.
func treeFiles(t *testing.T, dir, tree string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range strings.Split(mustGit(t, dir, "ls-tree", "-r", "--name-only", tree), "\n") {
		if p != "" {
			out[p] = mustGit(t, dir, "cat-file", "-p", tree+":"+p)
		}
	}
	return out
}

func TestSnapshotOfACleanTreeIsHEADs(t *testing.T) {
	root := newRepo(t)
	before := observe(t, root)
	tree, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustGit(t, root, "rev-parse", "HEAD^{tree}"); tree != want {
		t.Errorf("tree = %s, want HEAD's %s", tree, want)
	}
	before.same(t, root)
}

func TestSnapshotRecordsEveryChangeAndTouchesNothing(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "keep.txt", "keep\n")
	writeFile(t, root, "gone.txt", "gone\n")
	writeFile(t, root, ".gitignore", "*.log\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "files")

	writeFile(t, root, "README.md", "edited, unstaged\n")
	writeFile(t, root, "keep.txt", "edited, staged\n")
	mustGit(t, root, "add", "keep.txt")
	writeFile(t, root, "keep.txt", "edited again after staging\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "new/deep/file.txt", "untracked\n")
	writeFile(t, root, "debug.log", "ignored\n")

	before := observe(t, root)
	tree, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before.same(t, root)
	want := map[string]string{
		".gitignore":        "*.log",
		"README.md":         "edited, unstaged",
		"keep.txt":          "edited again after staging",
		"new/deep/file.txt": "untracked",
	}
	if got := treeFiles(t, root, tree); !reflect.DeepEqual(got, want) {
		t.Errorf("tree files = %v, want %v", got, want)
	}
	// the same tree twice is the same hash
	again, err := Snapshot(ctx, root)
	if err != nil || again != tree {
		t.Errorf("second snapshot = %s, %v; want %s", again, err, tree)
	}
}

func TestSnapshotWithoutAnIndex(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "extra.txt", "extra\n")
	if err := os.Remove(filepath.Join(root, ".git", "index")); err != nil {
		t.Fatal(err)
	}
	tree, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "index")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the snapshot wrote a real index")
	}
	if got := treeFiles(t, root, tree); !reflect.DeepEqual(got, map[string]string{"README.md": "hello", "extra.txt": "extra"}) {
		t.Errorf("tree files = %v", got)
	}
}

func TestSnapshotOnAnUnbornBranch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "init", "-q", "-b", "main")
	writeFile(t, root, "a.txt", "a\n")
	before := observe(t, root)
	tree, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before.same(t, root)
	if got := treeFiles(t, root, tree); !reflect.DeepEqual(got, map[string]string{"a.txt": "a"}) {
		t.Errorf("tree files = %v", got)
	}
}

func TestSnapshotInALinkedWorktree(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(31, "Snap")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "card.txt", "card\n")
	mainBefore, cardBefore := observe(t, root), observe(t, p)
	tree, err := Snapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	mainBefore.same(t, root)
	cardBefore.same(t, p)
	if got := treeFiles(t, p, tree)["card.txt"]; got != "card" {
		t.Errorf("card.txt = %q", got)
	}
}

func TestDiffTreesAndRevertTo(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "edit.txt", "one\n")
	writeFile(t, root, "del.txt", "del\n")
	writeFile(t, root, "dir/file.txt", "was a file in a dir\n")
	writeFile(t, root, "flat", "was a file\n")
	writeFile(t, root, "glob[1].txt", "literal\n")
	writeFile(t, root, "bin.dat", "\x00\x01\x02")
	writeFile(t, root, "same.txt", "same\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "start")
	// a staged edit from before the run stays staged through the revert
	writeFile(t, root, "same.txt", "staged before the run\n")
	mustGit(t, root, "add", "same.txt")

	start, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	// what the run did
	writeFile(t, root, "edit.txt", "two\n")
	if err := os.Remove(filepath.Join(root, "del.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "dir", "now a file\n")
	if err := os.Remove(filepath.Join(root, "flat")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "flat/inner.txt", "now a dir\n")
	writeFile(t, root, "glob[1].txt", "changed\n")
	writeFile(t, root, "glob1.txt", "a sibling a glob would match\n")
	writeFile(t, root, "bin.dat", "\x00\x09")
	writeFile(t, root, "brand/new/made.txt", "created\n")

	now, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	patch, paths, err := DiffTrees(ctx, root, start, now)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"bin.dat", "brand/new/made.txt", "del.txt", "dir", "dir/file.txt", "edit.txt", "flat", "flat/inner.txt", "glob1.txt", "glob[1].txt"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("paths = %q\nwant %q", paths, wantPaths)
	}
	for _, want := range []string{"-one", "+two", "GIT binary patch", "+created"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch lacks %q:\n%s", want, patch)
		}
	}
	if _, empty, err := DiffTrees(ctx, root, start, start); err != nil || len(empty) != 0 {
		t.Errorf("a tree against itself = %q, %v", empty, err)
	}

	head := mustGit(t, root, "rev-parse", "HEAD")
	staged := mustGit(t, root, "diff", "--cached", "--name-only")
	if err := RevertTo(ctx, root, start, paths); err != nil {
		t.Fatal(err)
	}
	back, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if back != start {
		_, left, _ := DiffTrees(ctx, root, start, back)
		t.Errorf("after revert the tree differs from the start in %q", left)
	}
	if _, err := os.Stat(filepath.Join(root, "brand")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the directory the run created is still there")
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != head {
		t.Error("revert moved HEAD")
	}
	if got := mustGit(t, root, "diff", "--cached", "--name-only"); got != staged {
		t.Errorf("revert changed the index: %q → %q", staged, got)
	}
}

// TestRestoreIndexPutsBackWhatTheRunStaged: what a run (or the session
// after it) staged of the paths a revert puts back is unstaged with them —
// a created file leaves the index, an edited one is staged as it was when
// the run started — so the next commit cannot bring the reverted work
// back; an entry staged before the run, and paths the run never touched,
// keep theirs. IndexTree reads the index without writing it.
func TestRestoreIndexPutsBackWhatTheRunStaged(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "edit.txt", "one\n")
	writeFile(t, root, "other.txt", "other\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "start")
	writeFile(t, root, "edit.txt", "staged before the run\n")
	mustGit(t, root, "add", "edit.txt")

	before := observe(t, root)
	index, err := IndexTree(ctx, root)
	if err != nil || index == "" {
		t.Fatalf("IndexTree = %q, %v", index, err)
	}
	before.same(t, root)
	start, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	// what the run did, all of it staged
	writeFile(t, root, "edit.txt", "the run's edit\n")
	writeFile(t, root, "made.txt", "made by the run\n")
	writeFile(t, root, "other.txt", "staged by a person, not the run's path\n")
	mustGit(t, root, "add", "-A")
	now, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_, paths, err := DiffTrees(ctx, root, start, now)
	if err != nil {
		t.Fatal(err)
	}
	paths = slices.DeleteFunc(paths, func(p string) bool { return p == "other.txt" })
	if err := RevertTo(ctx, root, start, paths); err != nil {
		t.Fatal(err)
	}
	if err := RestoreIndex(ctx, root, index, paths); err != nil {
		t.Fatal(err)
	}
	status := mustGit(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	if want := "M  edit.txt\nM  other.txt"; status != want {
		t.Errorf("status after the revert:\n%s\nwant:\n%s", status, want)
	}
	if got := mustGit(t, root, "show", ":edit.txt"); got != "staged before the run" {
		t.Errorf("edit.txt is staged as %q", got)
	}
	// nothing to do is no error, and a path in neither is left be
	if err := RestoreIndex(ctx, root, index, []string{"never.txt"}); err != nil {
		t.Errorf("a path in neither: %v", err)
	}
}

func TestRevertToRefusesAPathOutsideTheTree(t *testing.T) {
	root := newRepo(t)
	start, err := Snapshot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "README.md", "changed\n")
	for _, bad := range [][]string{{"../outside"}, {"README.md", "/etc/passwd"}, {"a/../../b"}} {
		if err := RevertTo(ctx, root, start, bad); err == nil {
			t.Errorf("RevertTo(%q) went ahead", bad)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "README.md")); string(b) != "changed\n" {
		t.Error("a refused revert touched a file")
	}
	if err := RevertTo(ctx, root, start, nil); err != nil {
		t.Errorf("no paths: %v", err)
	}
}

// An ignored file on disk when a run starts stays out of the run's reading
// of the tree, whatever .gitignore says later: a step that un-ignores it
// neither puts its contents into the diff nor makes it look created, and a
// revert never deletes it.
func TestAnIgnoredFileStaysOutOfTheRunsSnapshotsAndIsNeverReverted(t *testing.T) {
	root := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\nbuild/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", ".gitignore")
	mustGit(t, root, "commit", "-q", "-m", "ignore")
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build", "out.o"), []byte("obj\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep, err := Ignored(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keep, []string{".env", "build"}) {
		t.Fatalf("ignored = %q", keep)
	}
	start, err := Snapshot(ctx, root, keep...)
	if err != nil {
		t.Fatal(err)
	}
	// a step un-ignores both, and edits a tracked file
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := observe(t, root)
	end, err := Snapshot(ctx, root, keep...)
	if err != nil {
		t.Fatal(err)
	}
	before.same(t, root)
	patch, paths, err := DiffTrees(ctx, root, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{".gitignore"}) {
		t.Errorf("paths = %q, want only .gitignore", paths)
	}
	if strings.Contains(patch, "s3cret") || strings.Contains(patch, "obj") {
		t.Errorf("an ignored file's contents leaked into the diff:\n%s", patch)
	}
	// even handed the paths, a revert keeps what was on disk at the start
	if err := RevertTo(ctx, root, start, []string{".env", ".gitignore", "build/out.o"}, keep...); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, ".env")); err != nil || string(raw) != "TOKEN=s3cret\n" {
		t.Errorf(".env after the revert: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(root, "build", "out.o")); err != nil {
		t.Errorf("build/out.o after the revert: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(raw) != ".env\nbuild/\n" {
		t.Errorf(".gitignore was not put back: %q", raw)
	}
}

// TestAFileWrittenBesideOnlyIgnoredFilesIsTheRuns: git lists an untracked
// directory holding only ignored files as a directory, though nothing
// ignores it. Kept out whole, a file a run wrote there was in no
// snapshot — not in its diff, nor reverted. Only its ignored files are
// kept out.
func TestAFileWrittenBesideOnlyIgnoredFilesIsTheRuns(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gitignore", "*.log\n")
	mustGit(t, root, "add", ".gitignore")
	mustGit(t, root, "commit", "-q", "-m", "ignore")
	writeFile(t, root, "logs/x.log", "log\n")
	keep, err := Ignored(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keep, []string{"logs/x.log"}) {
		t.Fatalf("ignored = %q", keep)
	}
	start, err := Snapshot(ctx, root, keep...)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "logs/notes.md", "the run's\n")
	end, err := Snapshot(ctx, root, keep...)
	if err != nil {
		t.Fatal(err)
	}
	_, paths, err := DiffTrees(ctx, root, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"logs/notes.md"}) {
		t.Fatalf("the run's diff names %q", paths)
	}
	if err := RevertTo(ctx, root, start, paths, keep...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "notes.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the run's file outlived its revert: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "x.log")); err != nil {
		t.Errorf("the ignored file went with it: %v", err)
	}
}
