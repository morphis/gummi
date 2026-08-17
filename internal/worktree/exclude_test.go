package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGummiExcludedIdempotent(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	for i := 0; i < 2; i++ {
		if untracked, err := m.EnsureGummiExcluded(ctx); err != nil || untracked {
			t.Fatalf("pass %d: untracked=%v err=%v", i, untracked, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), gummiExcludeLine+"\n"); got != 1 {
		t.Fatalf("exclude line appears %d times:\n%s", got, b)
	}
	// the exclusion is effective: a new .gummi file is invisible to status
	writeFile(t, root, ".gummi/config.yaml", "cfg\n")
	if out := mustGit(t, root, "status", "--porcelain"); out != "" {
		t.Errorf(".gummi not excluded: %q", out)
	}
}

func TestEnsureGummiExcludedUntracksWithoutTouchingDisk(t *testing.T) {
	root := newRepo(t)
	// the poisoned state: an agent once committed .gummi into the repo
	writeFile(t, root, ".gummi/seq", "3\n")
	writeFile(t, root, ".gummi/config.yaml", "cfg\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")

	m := newManager(t, root)
	untracked, err := m.EnsureGummiExcluded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !untracked {
		t.Fatal("tracked .gummi not reported as untracked")
	}
	if out := mustGit(t, root, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi still tracked: %q", out)
	}
	for _, f := range []string{".gummi/seq", ".gummi/config.yaml"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s removed from disk: %v", f, err)
		}
	}
}

// TestLandAfterUntrack is the drive's deadlock regression: with .gummi/seq
// tracked, every seq bump dirtied the main checkout and the merge guard
// refused every land. After launch untracks .gummi, a land succeeds with
// no stash.
func TestLandAfterUntrack(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gummi/seq", "1\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")

	m, f, _ := committedFeature(t, root)
	writeFile(t, root, ".gummi/seq", "2\n") // the bump that used to deadlock

	// half the fix: the merge guard no longer counts .gummi churn as
	// dirtiness, even while seq is still tracked
	if dirty, err := m.MainTrackedDirty(ctx); err != nil || dirty {
		t.Fatalf("a tracked-seq bump still blocks the merge guard: dirty=%v err=%v", dirty, err)
	}
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	if dirty, err := m.MainTrackedDirty(ctx); err != nil || dirty {
		t.Fatalf("staged .gummi deletion must not block a land: dirty=%v err=%v", dirty, err)
	}
	if _, err := m.SquashMerge(ctx, f, "land feature"); err != nil {
		t.Fatalf("land failed after untrack: %v", err)
	}
	if out := mustGit(t, root, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi re-entered tracking via the land: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "seq")); err != nil {
		t.Errorf("seq removed from disk: %v", err)
	}
}

// TestCreateUntracksGummiInWorktree: when main's HEAD still carries
// .gummi content (the launch untracking is index-only, so HEAD keeps it
// until the user's next commit), a fresh worktree checks those files out
// tracked in its own index — where the info/exclude rule is powerless.
// Create must untrack them there too, without touching disk, and leave
// the worktree clean.
func TestCreateUntracksGummiInWorktree(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gummi/seq", "3\n")
	writeFile(t, root, ".gummi/config.yaml", "cfg\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")

	m := newManager(t, root)
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	f := feature(5, "Fresh worktree")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if out := mustGit(t, p, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi tracked in new worktree: %q", out)
	}
	if out := mustGit(t, p, "status", "--porcelain"); out != "" {
		t.Errorf("new worktree not clean: %q", out)
	}
	// index-only, exactly like the launch pass: the checkout's files stay
	if _, err := os.Stat(filepath.Join(p, ".gummi", "seq")); err != nil {
		t.Errorf("worktree .gummi/seq removed from disk: %v", err)
	}
	// a modified .gummi file no longer reaches an agent's bulk add
	writeFile(t, p, ".gummi/seq", "4\n")
	mustGit(t, p, "add", "-A")
	if out := mustGit(t, p, "status", "--porcelain"); out != "" {
		t.Errorf(".gummi churn swept in by add -A: %q", out)
	}
}

// TestLandAfterWorktreeUntrack: the untrack commit Create leaves on the
// branch must merge cleanly against main's own staged .gummi deletion —
// both sides agree the files stop being tracked — and main's on-disk
// .gummi (the live workspace) must survive the land.
func TestLandAfterWorktreeUntrack(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gummi/seq", "1\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")

	m := newManager(t, root)
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	f := feature(6, "Land after untrack")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "work.txt", "feature work\n")
	if made, err := m.CommitAll(ctx, f, "checkpoint"); err != nil || !made {
		t.Fatalf("checkpoint: made=%v err=%v", made, err)
	}
	if _, err := m.SquashMerge(ctx, f, "land feature"); err != nil {
		t.Fatalf("land failed: %v", err)
	}
	if out := mustGit(t, root, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi still tracked in main after land: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "seq")); err != nil {
		t.Errorf("live .gummi/seq removed from main by the land: %v", err)
	}
}

// TestRebaseAfterWorktreeUntrack: once main commits its own .gummi
// deletion and advances, the branch's untrack commit becomes empty and a
// rebase onto main must pass through it cleanly.
func TestRebaseAfterWorktreeUntrack(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gummi/seq", "1\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")

	m := newManager(t, root)
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	f := feature(7, "Rebase after untrack")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "work.txt", "feature work\n")
	if _, err := m.CommitAll(ctx, f, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	// main commits the staged deletion and moves on
	mustGit(t, root, "commit", "-q", "-m", "user commit carrying the untracking")
	writeFile(t, root, "main.txt", "main work\n")
	mustGit(t, root, "add", "main.txt")
	mustGit(t, root, "commit", "-q", "-m", "main advances")
	if err := m.RebaseOnMain(ctx, f); err != nil {
		t.Fatalf("rebase failed: %v", err)
	}
	if ok, err := m.RebasedOnMain(ctx, f); err != nil || !ok {
		t.Fatalf("branch not rebased on main: ok=%v err=%v", ok, err)
	}
	if out := mustGit(t, p, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi re-entered worktree tracking via the rebase: %q", out)
	}
}
