package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// committedFeature creates a feature worktree with one committed file and
// returns the manager, feature, and worktree path.
func committedFeature(t *testing.T, root string) (*Manager, *domain.Feature, string) {
	t.Helper()
	m := newManager(t, root)
	f := feature(9, "Land me")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "sq.txt", "feature work\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature commit")
	return m, f, p
}

func TestSquashMerge(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)

	msg := "FD-009: land me\n\nAdds sq.txt with the feature work."
	if _, err := m.SquashMerge(ctx, f, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sq.txt")); err != nil {
		t.Errorf("merged file missing from main checkout: %v", err)
	}
	if got := mustGit(t, root, "log", "-1", "--format=%B"); strings.TrimSpace(got) != msg {
		t.Errorf("commit message = %q, want %q", got, msg)
	}
	if landed, err := m.Landed(ctx, f); !landed || err != nil {
		t.Errorf("Landed after squash merge = %v, %v; want true", landed, err)
	}
	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout not clean after merge:\n%s", out)
	}
	// a second merge of the same branch has nothing left to add
	if _, err := m.SquashMerge(ctx, f, msg); err == nil {
		t.Error("re-merging an already-landed branch accepted")
	}
}

// TestSquashMergeReturnsSha proves the sha plumbing behind the merged event:
// a successful squash merge returns the newly created commit's full 40-hex
// sha, and the already-landed / empty-message failure paths return "" (no
// commit was created).
func TestSquashMergeReturnsSha(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)

	sha, err := m.SquashMerge(ctx, f, "FD-009: land me")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		t.Errorf("sha = %q, want a 40-hex sha", sha)
	}
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != sha {
		t.Errorf("returned sha %q != HEAD %q", sha, got)
	}

	// already-landed: the branch's content is in main, nothing to merge
	if sha, err := m.SquashMerge(ctx, f, "FD-009: again"); err == nil {
		t.Error("re-merging an already-landed branch accepted")
	} else if sha != "" {
		t.Errorf("already-landed sha = %q, want empty", sha)
	}

	// empty message: refused before any commit
	if sha, err := m.SquashMerge(ctx, f, ""); err == nil {
		t.Error("empty message accepted")
	} else if sha != "" {
		t.Errorf("empty-message sha = %q, want empty", sha)
	}
}

func TestSquashMergeConflict(t *testing.T) {
	root := newRepo(t)
	m, f, p := committedFeature(t, root)
	// both sides rewrite README.md
	writeFile(t, p, "README.md", "branch version\n")
	mustGit(t, p, "commit", "-qam", "branch readme")
	writeFile(t, root, "README.md", "main version\n")
	mustGit(t, root, "commit", "-qam", "main readme")
	mainHead := mustGit(t, root, "rev-parse", "HEAD")

	_, err := m.SquashMerge(ctx, f, "FD-009: conflicting")
	var ce *MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *MergeConflictError", err)
	}
	if len(ce.Files) != 1 || ce.Files[0] != "README.md" {
		t.Errorf("conflicted files = %v, want [README.md]", ce.Files)
	}
	// main is unwound: same HEAD, clean status
	if got := mustGit(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main HEAD moved by failed merge: %s -> %s", mainHead, got)
	}
	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout dirty after undone merge:\n%s", out)
	}
}

func TestSquashMergeDirtyMainRefused(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	writeFile(t, root, "README.md", "local edit\n")

	_, err := m.SquashMerge(ctx, f, "FD-009: land me")
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v, want dirty-main refusal", err)
	}
	if got := mustGit(t, root, "status", "--porcelain"); !strings.Contains(got, "README.md") {
		t.Errorf("local edit lost: status = %q", got)
	}
	if dirty, err := m.MainTrackedDirty(ctx); !dirty || err != nil {
		t.Errorf("MainTrackedDirty = %v, %v; want true", dirty, err)
	}
}

func TestSquashMergeUntrackedMainFileOK(t *testing.T) {
	// untracked files in main don't block the merge (git handles the
	// overwrite case itself; a non-colliding file is simply fine)
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	writeFile(t, root, "scratch.txt", "untracked\n")

	if dirty, err := m.MainTrackedDirty(ctx); dirty || err != nil {
		t.Errorf("MainTrackedDirty with only untracked file = %v, %v; want false", dirty, err)
	}
	if _, err := m.SquashMerge(ctx, f, "FD-009: land me"); err != nil {
		t.Fatal(err)
	}
}

func TestSquashMergeNothingToMerge(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(9, "Land me")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	// fresh branch: no commits of its own
	if _, err := m.SquashMerge(ctx, f, "FD-009: nothing"); err == nil || !strings.Contains(err.Error(), "no commits to merge") {
		t.Fatalf("err = %v, want no-commits refusal", err)
	}
}

func TestSquashMergeEmptyMessageRefused(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)
	for _, msg := range []string{"", "   \n\t"} {
		if _, err := m.SquashMerge(ctx, f, msg); err == nil {
			t.Errorf("empty message %q accepted", msg)
		}
	}
}

func TestSquashMergeMissingBranch(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(9, "Land me")
	if _, err := m.SquashMerge(ctx, f, "FD-009: land me"); err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Fatalf("err = %v, want missing-branch refusal", err)
	}
}

func TestDeleteLandedBranch(t *testing.T) {
	root := newRepo(t)
	m, f, _ := committedFeature(t, root)

	// unmerged branch: refused (worktree still pins it too, but -d's
	// merged-check fires first on a detached copy — remove the worktree
	// so the refusal is purely the merged-check)
	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteLandedBranch(ctx, f); err == nil {
		t.Fatal("unmerged branch deleted")
	}

	// squash-merge it through the real path: commits aren't ancestors, so
	// plain -d would refuse, but the recorded landed sha clears the
	// force-delete.
	if _, err := m.SquashMerge(ctx, f, "FD-009: land me"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteLandedBranch(ctx, f); err != nil {
		t.Fatalf("landed squash-merged branch not deleted: %v", err)
	}
	if ok, err := m.BranchExists(ctx, f); ok || err != nil {
		t.Errorf("BranchExists after delete = %v, %v; want false", ok, err)
	}
}
