package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGummiExcludedIdempotent(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
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

	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := m.SquashMerge(ctx, f, "land feature"); err != nil {
		t.Fatalf("land failed after untrack: %v", err)
	}
	if out := mustGit(t, root, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi re-entered tracking via the land: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "seq")); err != nil {
		t.Errorf("seq removed from disk: %v", err)
	}
}

// TestCommitFileForcesPastExclusion: gummi's own artifact commits (the
// spec on the feature branch) must survive the repo-wide exclusion.
func TestCommitFileForcesPastExclusion(t *testing.T) {
	root := newRepo(t)
	m, err := NewManager(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	f := feature(4, "Spec carrier")
	if _, err := m.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".gummi", "specs", "FD-004-spec-carrier.md")
	if err := m.CommitFile(ctx, f, rel, "# spec\n", "FD-004: spec"); err != nil {
		t.Fatal(err)
	}
	committed, err := m.FileCommitted(ctx, f, rel)
	if err != nil || !committed {
		t.Fatalf("spec artifact not committed past the exclusion: %v %v", committed, err)
	}
}
