package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphia/gummi/internal/domain"
)

var ctx = context.Background()

// newRepo creates a throwaway repo with one commit and returns its root.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// t.TempDir can contain symlinked components on some platforms;
	// resolve so path comparisons match git's physical paths.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "user.name", "test")
	mustGit(t, root, "config", "user.email", "test@example.invalid")
	writeFile(t, root, "README.md", "hello\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "initial")
	return root
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(ctx, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFile(t *testing.T, root string, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func feature(num int, title string) *domain.Feature {
	id, _ := domain.NewFeatureID(num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return &domain.Feature{
		ID: id, Num: num, Title: title, Slug: slug,
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
}

func TestNewManagerRejectsNonRepo(t *testing.T) {
	if _, err := NewManager(ctx, t.TempDir()); err == nil {
		t.Fatal("non-repo accepted")
	}
}

func TestNewManagerRejectsSubdir(t *testing.T) {
	root := newRepo(t)
	sub := filepath.Join(root, "subdir")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(ctx, sub); err == nil {
		t.Fatal("subdir accepted as repo root")
	}
}

func TestCreateAndRemove(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f := feature(42, "Dark mode")

	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".gummi", "worktrees", "FD-042"); p != want {
		t.Errorf("path = %s, want %s", p, want)
	}
	if _, err := os.Stat(filepath.Join(p, "README.md")); err != nil {
		t.Errorf("worktree missing checked-out file: %v", err)
	}
	if got := mustGit(t, p, "rev-parse", "--abbrev-ref", "HEAD"); got != "gummi/FD-042-dark-mode" {
		t.Errorf("worktree branch = %s", got)
	}
	if ok, err := m.Exists(ctx, f); !ok || err != nil {
		t.Errorf("Exists = %v, %v; want true", ok, err)
	}

	// duplicate create must fail deterministically
	if _, err := m.Create(ctx, f); err == nil {
		t.Fatal("duplicate create accepted")
	}

	// give the branch a commit of its own so it counts as unmerged
	writeFile(t, p, "work.txt", "wip\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "wip")

	if err := m.Remove(ctx, f, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Exists(ctx, f); ok {
		t.Error("worktree still exists after Remove")
	}
	// branch survives Remove, then delete it (merged? no → -d fails, -D works)
	if err := m.DeleteBranch(ctx, f, false); err == nil {
		t.Error("unmerged branch deleted without force")
	}
	if err := m.DeleteBranch(ctx, f, true); err != nil {
		t.Errorf("force branch delete: %v", err)
	}
}

func TestCreateInEmptyRepoFails(t *testing.T) {
	root := t.TempDir()
	mustGit(t, root, "init", "-q", "-b", "main")
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, feature(1, "Too early")); err == nil {
		t.Fatal("create in commitless repo accepted")
	}
}

func TestDirty(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(1, "CSV export")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if dirty, err := m.Dirty(ctx, f); dirty || err != nil {
		t.Fatalf("fresh worktree dirty=%v err=%v, want clean", dirty, err)
	}
	writeFile(t, p, "new.txt", "x\n")
	if dirty, err := m.Dirty(ctx, f); !dirty || err != nil {
		t.Fatalf("untracked file: dirty=%v err=%v, want true", dirty, err)
	}
	// dirty remove refused without force, allowed with force
	if err := m.Remove(ctx, f, false); err == nil {
		t.Fatal("dirty worktree removed without force")
	}
	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatal(err)
	}
}

func TestLanded(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(7, "Auth fix")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "fix.txt", "done\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature work")

	if landed, err := m.Landed(ctx, f); landed || err != nil {
		t.Fatalf("unmerged branch landed=%v err=%v, want false", landed, err)
	}
	mustGit(t, root, "merge", "-q", "--no-ff", "-m", "merge feature", f.BranchName())
	if landed, err := m.Landed(ctx, f); !landed || err != nil {
		t.Fatalf("merged branch landed=%v err=%v, want true", landed, err)
	}
	// now branch is merged: safe delete must succeed after removing worktree
	if err := m.Remove(ctx, f, false); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteBranch(ctx, f, false); err != nil {
		t.Errorf("merged branch safe-delete failed: %v", err)
	}
}

func TestRebaseOnMain(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(9, "Search")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// feature commit
	writeFile(t, p, "feature.txt", "feature\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature")
	// main advances independently
	writeFile(t, root, "main.txt", "main\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main advance")

	if err := m.RebaseOnMain(ctx, f); err != nil {
		t.Fatal(err)
	}
	// after rebase, main's commit is an ancestor of the feature branch
	if ok, err := gitOK(ctx, p, "merge-base", "--is-ancestor", "main", "HEAD"); !ok || err != nil {
		t.Fatalf("rebase did not put branch on top of main: ok=%v err=%v", ok, err)
	}
}

func TestRebaseConflictAbortsCleanly(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(10, "Conflict")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "README.md", "feature version\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature edit")
	writeFile(t, root, "README.md", "main version\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main edit")

	rerr := m.RebaseOnMain(ctx, f)
	if rerr == nil {
		t.Fatal("conflicting rebase reported success")
	}
	// the error names the conflicted file so the UI can surface it
	var ce *RebaseConflictError
	if !errors.As(rerr, &ce) {
		t.Fatalf("want *RebaseConflictError, got %T: %v", rerr, rerr)
	}
	if len(ce.Files) != 1 || ce.Files[0] != "README.md" {
		t.Errorf("conflicted files = %v, want [README.md]", ce.Files)
	}
	// worktree must not be left mid-rebase (.git is a file in a
	// worktree; ask git where the rebase state would live)
	rebaseDir := mustGit(t, p, "rev-parse", "--git-path", "rebase-merge")
	if !filepath.IsAbs(rebaseDir) {
		rebaseDir = filepath.Join(p, rebaseDir)
	}
	if _, err := os.Stat(rebaseDir); !os.IsNotExist(err) {
		t.Errorf("worktree left mid-rebase (%s exists, stat err=%v)", rebaseDir, err)
	}
	if dirty, err := m.Dirty(ctx, f); dirty || err != nil {
		t.Errorf("worktree dirty after aborted rebase: %v %v", dirty, err)
	}
}

func TestList(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	if paths, err := m.List(ctx); err != nil || len(paths) != 0 {
		t.Fatalf("List on fresh repo = %v, %v", paths, err)
	}
	if _, err := m.Create(ctx, feature(1, "One")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, feature(2, "Two")); err != nil {
		t.Fatal(err)
	}
	paths, err := m.List(ctx)
	if err != nil || len(paths) != 2 {
		t.Fatalf("List = %v, %v; want 2 entries", paths, err)
	}
	for _, p := range paths {
		if !strings.Contains(p, filepath.Join(".gummi", "worktrees", "FD-00")) {
			t.Errorf("unexpected worktree path %s", p)
		}
	}
}

func TestCreateAfterRemoveNamesLeftoverBranch(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(3, "Recreate")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, f, false); err != nil {
		t.Fatal(err)
	}
	_, err := m.Create(ctx, f)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("recreate error = %v, want leftover-branch explanation", err)
	}
	// after deleting the branch, recreate succeeds
	if err := m.DeleteBranch(ctx, f, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatalf("recreate after branch delete: %v", err)
	}
}

func TestRebaseOnDirtyWorktreeIsNotScary(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(4, "Dirty rebase")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// make main advance and dirty the worktree with a tracked-file edit
	writeFile(t, root, "main.txt", "advance\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "advance")
	writeFile(t, p, "README.md", "uncommitted edit\n")

	err = m.RebaseOnMain(ctx, f)
	if err == nil {
		t.Fatal("rebase on dirty worktree succeeded unexpectedly")
	}
	if strings.Contains(err.Error(), "manual attention") {
		t.Errorf("mundane dirty-worktree failure produced scary error: %v", err)
	}
}

func TestSymlinkedWorktreesDirRefused(t *testing.T) {
	root := newRepo(t)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".gummi"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, ".gummi", "worktrees")); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, feature(5, "Escape attempt")); err == nil {
		t.Fatal("Create wrote through a symlinked worktrees dir")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("symlink target contaminated: %v", entries)
	}
}

func TestMissingWorktreeErrorsClearly(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)
	f := feature(6, "Ghost")
	if _, err := m.Dirty(ctx, f); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Errorf("Dirty on missing worktree: %v, want 'no worktree' error", err)
	}
	if err := m.RebaseOnMain(ctx, f); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Errorf("Rebase on missing worktree: %v, want 'no worktree' error", err)
	}
}

// The injection tests: hostile data must be stopped at featurePaths, before
// any git command runs.
func TestHostileFeatureRefused(t *testing.T) {
	root := newRepo(t)
	m, _ := NewManager(ctx, root)

	hostile := []*domain.Feature{
		func() *domain.Feature { f := feature(1, "ok"); f.Slug = "../../../tmp/evil"; return f }(),
		func() *domain.Feature { f := feature(1, "ok"); f.Slug = "-rf"; return f }(),
		func() *domain.Feature { f := feature(1, "ok"); f.Slug = "a;rm -rf"; return f }(),
		func() *domain.Feature { f := feature(1, "ok"); f.ID = "FD-001/../.."; return f }(),
		func() *domain.Feature { f := feature(1, "ok"); f.ID = "--upload-pack=evil"; return f }(),
		func() *domain.Feature { f := feature(1, "ok"); f.Title = ""; return f }(),
	}
	for i, f := range hostile {
		if _, err := m.Create(ctx, f); err == nil {
			t.Errorf("hostile feature %d accepted by Create", i)
		}
		if err := m.Remove(ctx, f, true); err == nil {
			t.Errorf("hostile feature %d accepted by Remove", i)
		}
		if _, err := m.Dirty(ctx, f); err == nil {
			t.Errorf("hostile feature %d accepted by Dirty", i)
		}
		if err := m.RebaseOnMain(ctx, f); err == nil {
			t.Errorf("hostile feature %d accepted by Rebase", i)
		}
	}
	// nothing may have been created
	if entries, _ := os.ReadDir(filepath.Join(root, ".gummi", "worktrees")); len(entries) != 0 {
		t.Errorf("hostile input left artifacts: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(os.TempDir(), "evil")); err == nil {
		t.Error("path traversal escaped the repo")
	}
}
