package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestEnsureScratchDetachedAndIdempotent: the scratch tree lands at
// .gummi/scratch/<ID>, checks out main's HEAD on a detached head (so
// nothing done in it can land on a branch), and a second call for the
// same card returns that same tree rather than failing on the existing
// path — one tree per card, shared by all of its design stages.
func TestEnsureScratchDetachedAndIdempotent(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")
	f.Stage = domain.StageSpec

	p, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatalf("EnsureScratch: %v", err)
	}
	want := filepath.Join(root, ".gummi", "scratch", string(f.ID))
	if p != want {
		t.Fatalf("scratch path = %s, want %s", p, want)
	}
	if _, err := os.Stat(filepath.Join(p, "README.md")); err != nil {
		t.Fatalf("scratch tree is not a checkout of main: %v", err)
	}
	// symbolic-ref exits non-zero on a detached HEAD — that is the assertion.
	if onBranch, err := gitOK(ctx, p, "symbolic-ref", "-q", "HEAD"); err != nil {
		t.Fatal(err)
	} else if onBranch {
		t.Fatalf("scratch HEAD is on branch %s, want detached",
			strings.TrimSpace(mustGit(t, p, "symbolic-ref", "--short", "HEAD")))
	}
	// A card's own branch name must remain free: the scratch tree is not
	// the card's work, and Create must still be able to cut gummi/FD-001-*.
	if ok, err := gitOK(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+f.BranchName()); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatalf("scratch tree consumed the card's branch %s", f.BranchName())
	}

	again, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatalf("second EnsureScratch: %v", err)
	}
	if again != p {
		t.Fatalf("second EnsureScratch = %s, want the same tree %s", again, p)
	}
}

// TestScratchTreeIsolatesWritesFromMain is the regression this whole
// mechanism exists for: an agent running ordinary build commands in its
// working directory (Go rewriting go.sum was the observed case) must not
// dirty the operator's checkout. The prompt fence it replaces could only
// ask; a separate tree makes it true.
func TestScratchTreeIsolatesWritesFromMain(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")

	// the launch's own hygiene pass: .gummi (and so the scratch tree under
	// it) is excluded from the repo, exactly as a real workspace has it.
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	before := mustGit(t, root, "status", "--porcelain", "--untracked-files=all")

	p, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "go.sum", "rewritten by the toolchain\n")
	writeFile(t, p, "README.md", "clobbered\n")

	if out := mustGit(t, root, "status", "--porcelain", "--untracked-files=all"); out != before {
		t.Fatalf("main checkout dirtied by writes in the scratch tree:\n%s", out)
	}
	// the tripwire's own input, which is what actually parks a card
	if paths, err := m.MainDirtyPaths(ctx); err != nil {
		t.Fatal(err)
	} else if len(paths) != 0 {
		t.Fatalf("MainDirtyPaths = %v, want none", paths)
	}
	// and main's copy of the clobbered file is untouched
	if body, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	} else if string(body) != "hello\n" {
		t.Fatalf("main README.md = %q, want it untouched", body)
	}
}

// TestScratchNotAFeatureWorktree: List enumerates the card branch
// worktrees under .gummi/worktrees. A scratch tree is a sibling, not one
// of those — every caller that walks that list (stale-worktree sweeps,
// board state) must keep seeing branch worktrees only.
func TestScratchNotAFeatureWorktree(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")
	if _, err := m.EnsureScratch(ctx, f); err != nil {
		t.Fatal(err)
	}
	paths, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("List = %v, want no feature worktrees", paths)
	}
	if ok, err := m.Exists(ctx, f); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("Exists reports a branch worktree for a card that only has a scratch tree")
	}
}

// TestRemoveScratchDiscards: the tree and everything in it goes, and a
// card that never had one (or already lost it) is not an error — callers
// sweep unconditionally.
func TestRemoveScratchDiscards(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")

	if err := m.RemoveScratch(ctx, f); err != nil {
		t.Fatalf("RemoveScratch on a card with no scratch tree: %v", err)
	}

	p, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "design-notes.md", "work that must not survive\n")
	if err := m.RemoveScratch(ctx, f); err != nil {
		t.Fatalf("RemoveScratch: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("scratch tree survived removal: %v", err)
	}
	if err := m.RemoveScratch(ctx, f); err != nil {
		t.Fatalf("second RemoveScratch: %v", err)
	}
	// and the card can start over from a clean tree
	if _, err := m.EnsureScratch(ctx, f); err != nil {
		t.Fatalf("EnsureScratch after removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p, "design-notes.md")); !os.IsNotExist(err) {
		t.Fatal("discarded scratch content came back in the new tree")
	}
}

// TestRemoveScratchAfterDirectoryVanished: the directory removed out of
// band leaves git admin metadata that would block the next EnsureScratch
// for that card forever. RemoveScratch reports success (there is nothing
// on disk), and the card can still get a fresh tree.
func TestRemoveScratchAfterDirectoryVanished(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")
	p, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveScratch(ctx, f); err != nil {
		t.Fatalf("RemoveScratch after the directory vanished: %v", err)
	}
	if _, err := m.EnsureScratch(ctx, f); err != nil {
		t.Fatalf("EnsureScratch after the directory vanished: %v", err)
	}
}

// TestScratchPathRefusesEscape: the scratch tree goes through the same
// chokepoint as the branch worktree, so an ID that would escape
// .gummi/scratch is refused rather than resolved.
func TestScratchPathRefusesEscape(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(1, "Dark mode")
	f.ID = domain.FeatureID("../../etc")
	if _, err := m.ScratchPath(f); err == nil {
		t.Fatal("escaping ID accepted")
	}
}

// TestEnsureScratchUntracksGummiOnDetachedHead: a repo that committed
// .gummi checks those files out tracked in every new checkout, where the
// info/exclude rule is powerless — the same hazard Create handles. The
// scratch tree gets the same treatment, and it must work on a detached
// HEAD: the untrack commit has no branch to advance, and simply rides
// along on the throwaway tree.
func TestEnsureScratchUntracksGummiOnDetachedHead(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, ".gummi/seq", "3\n")
	writeFile(t, root, ".gummi/config.yaml", "cfg\n")
	mustGit(t, root, "add", "-f", ".gummi")
	mustGit(t, root, "commit", "-q", "-m", "poisoned")
	mainHead := strings.TrimSpace(mustGit(t, root, "rev-parse", "HEAD"))

	m := newManager(t, root)
	if _, err := m.EnsureGummiExcluded(ctx); err != nil {
		t.Fatal(err)
	}
	f := feature(5, "Dark mode")
	p, err := m.EnsureScratch(ctx, f)
	if err != nil {
		t.Fatalf("EnsureScratch on a repo tracking .gummi: %v", err)
	}
	if out := mustGit(t, p, "ls-files", "--", ".gummi"); out != "" {
		t.Errorf(".gummi tracked in scratch tree: %q", out)
	}
	if out := mustGit(t, p, "status", "--porcelain"); out != "" {
		t.Errorf("scratch tree not clean: %q", out)
	}
	// index-only, exactly like the launch pass: the checkout's files stay
	if _, err := os.Stat(filepath.Join(p, ".gummi", "seq")); err != nil {
		t.Errorf("scratch .gummi/seq removed from disk: %v", err)
	}
	// the untrack commit stayed on the detached head — main did not move
	if now := strings.TrimSpace(mustGit(t, root, "rev-parse", "HEAD")); now != mainHead {
		t.Fatalf("main HEAD moved from %s to %s", mainHead, now)
	}
	if onBranch, err := gitOK(ctx, p, "symbolic-ref", "-q", "HEAD"); err != nil {
		t.Fatal(err)
	} else if onBranch {
		t.Fatal("the untrack commit put the scratch tree on a branch")
	}
}
