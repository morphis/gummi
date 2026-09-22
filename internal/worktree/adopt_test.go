package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// adoptedFeature is a card minted onto an existing branch, as cardmint
// would build it: the scheme says the name is stored, and Branch holds it.
func adoptedFeature(num int, title, branch string) *domain.Feature {
	f := feature(num, title)
	f.BranchScheme, f.Branch = domain.BranchSchemeAdopted, branch
	return f
}

// theirBranch cuts a branch in root with n commits on it — somebody
// else's work, made without gummi, which is the whole premise of
// adoption. It leaves the repo back on main.
func theirBranch(t *testing.T, root, name string, n int) {
	t.Helper()
	mustGit(t, root, "checkout", "-q", "-b", name)
	for i := range n {
		writeFile(t, root, "theirs.txt", strings.Repeat("x", i+1)+"\n")
		mustGit(t, root, "add", ".")
		mustGit(t, root, "commit", "-q", "-m", "their commit "+strconv.Itoa(i+1))
	}
	mustGit(t, root, "checkout", "-q", "main")
}

func TestAttachTakesAnExistingBranchWithItsWork(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 2)
	m := newManager(t, root)
	f := adoptedFeature(42, "Rework their thing", "feat/theirs")

	p, err := m.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".gummi", "worktrees", "FD-042"); p != want {
		t.Errorf("path = %s, want %s", p, want)
	}
	if got := mustGit(t, p, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/theirs" {
		t.Errorf("worktree branch = %s, want the adopted one", got)
	}
	// The inherited file is in the checkout: the card opens onto their
	// work, not onto an empty branch wearing their branch's name.
	if _, err := os.Stat(filepath.Join(p, "theirs.txt")); err != nil {
		t.Errorf("adopted worktree is missing the inherited work: %v", err)
	}
	// The fork point is where their branch actually forked, so the diff
	// below shows the whole inherited change rather than nothing at all.
	fork, err := m.ForkPoint(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustGit(t, root, "merge-base", "main", "feat/theirs"); fork != want {
		t.Errorf("fork point = %s, want the real merge base %s", fork, want)
	}
	diff, err := m.Diff(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "theirs.txt") {
		t.Errorf("an adopted card's first diff does not show the inherited work:\n%s", diff)
	}
}

func TestAttachRefusesWhatItCannotAdopt(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 1)

	// A branch checked out elsewhere: git refuses, and the refusal has to
	// name the directory holding it rather than repeat git's own message.
	mustGit(t, root, "checkout", "-q", "feat/theirs")
	m := newManager(t, root)
	_, err := m.Attach(ctx, adoptedFeature(1, "One", "feat/theirs"))
	if err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Errorf("attach to a checked-out branch: err = %v, want one naming the checkout", err)
	}
	mustGit(t, root, "checkout", "-q", "main")

	// A branch that is not there at all.
	_, err = m.Attach(ctx, adoptedFeature(2, "Two", "feat/absent"))
	if err == nil || !strings.Contains(err.Error(), "no local branch") {
		t.Errorf("attach to a missing branch: err = %v, want a refusal saying so", err)
	}

	// A card that was never adopted must not reach this path: it would
	// attach to a branch nothing had cut.
	if _, err := m.Attach(ctx, feature(3, "Ordinary")); err == nil {
		t.Error("Attach accepted a card that was not minted onto a branch")
	}
}

// TestAnAdoptedBranchIsNeverDeleted is the custody guard of DESIGN §10
// D22. Both deletion paths refuse, including the landed one — "its
// content is on main" is an argument about content, and the ref is still
// somebody else's.
func TestAnAdoptedBranchIsNeverDeleted(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 1)
	m := newManager(t, root)
	f := adoptedFeature(42, "Rework their thing", "feat/theirs")
	if _, err := m.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, f, true); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		del  func() error
	}{
		{"DeleteBranch", func() error { return m.DeleteBranch(ctx, f, true) }},
		{"DeleteLandedBranch", func() error { return m.DeleteLandedBranch(ctx, f) }},
	} {
		if err := tc.del(); !errors.Is(err, ErrAdoptedBranch) {
			t.Errorf("%s on an adopted card: err = %v, want ErrAdoptedBranch", tc.name, err)
		}
	}
	if ok, err := m.BranchExistsNamed(ctx, "feat/theirs"); err != nil || !ok {
		t.Errorf("the adopted branch is gone (exists = %v, err = %v) — gummi did not cut it and must not destroy it", ok, err)
	}
}

// TestRecreateWillNotInventAnAdoptedBranch covers the quiet catastrophe:
// for an ordinary card, rebuilding a lost worktree cuts a fresh branch,
// which is right. Doing that for an adopted card would produce an empty
// branch wearing the name of the inherited work, indistinguishable
// downstream from the real thing.
func TestRecreateWillNotInventAnAdoptedBranch(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 1)
	m := newManager(t, root)
	f := adoptedFeature(42, "Rework their thing", "feat/theirs")
	p, err := m.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// the worktree and the branch both vanish out of band
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "worktree", "prune")
	mustGit(t, root, "branch", "-D", "feat/theirs")

	if _, err := m.Recreate(ctx, f); err == nil {
		t.Fatal("Recreate cut a fresh branch for an adopted card, silently replacing the inherited work")
	} else if !strings.Contains(err.Error(), "reflog") {
		t.Errorf("err = %v, want one pointing at how to recover the ref", err)
	}
}

func TestInspectBranchReportsWhatIsInherited(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 2)
	// main moves on underneath them: an inherited branch is usually old,
	// and saying how old is gummi's whole answer to that (D22).
	writeFile(t, root, "main.txt", "moved on\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "main moves")
	m := newManager(t, root)

	w, err := m.InspectBranch(ctx, "feat/theirs", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Commits) != 2 {
		t.Errorf("commits = %d (%v), want the 2 on their branch", len(w.Commits), w.Commits)
	}
	if !strings.Contains(w.Stat, "theirs.txt") {
		t.Errorf("stat = %q, want the inherited file", w.Stat)
	}
	if w.Behind != 1 {
		t.Errorf("behind = %d, want 1", w.Behind)
	}
	if !strings.Contains(w.Staleness(), "1 commit behind main") {
		t.Errorf("staleness = %q", w.Staleness())
	}

	// A branch with nothing of its own is a typo or a stale ref, and
	// adopting it would make a card whose whole premise is false.
	mustGit(t, root, "branch", "feat/empty", "main")
	if _, err := m.InspectBranch(ctx, "feat/empty", "main"); err == nil {
		t.Error("a branch with no commits of its own was accepted for adoption")
	}
	if _, err := m.InspectBranch(ctx, "main", "main"); err == nil {
		t.Error("adopting the branch the card lands on was accepted")
	}
}

func TestBehindBaseCountsOnlyWhatTheBaseHas(t *testing.T) {
	root := newRepo(t)
	theirBranch(t, root, "feat/theirs", 1)
	m := newManager(t, root)
	f := adoptedFeature(42, "Rework", "feat/theirs")
	if _, err := m.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	if got := m.BehindBase(ctx, f); got != 0 {
		t.Errorf("behind = %d on a current branch, want 0", got)
	}
	for range 3 {
		writeFile(t, root, "main.txt", time.Now().String())
		mustGit(t, root, "add", ".")
		mustGit(t, root, "commit", "-q", "-m", "main moves")
	}
	if got := m.BehindBase(ctx, f); got != 3 {
		t.Errorf("behind = %d after main took 3 commits, want 3", got)
	}
}
