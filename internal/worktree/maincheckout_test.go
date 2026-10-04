package worktree

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func mainCheckoutFeature(num int) *domain.Feature {
	id, _ := domain.NewID(domain.KindFreeform, num)
	slug, _ := domain.Slugify("poke at the pty leak")
	return &domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: "poke at the pty leak", Slug: slug,
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind,
		MainCheckout: true,
	}
}

// TestAMainCheckoutCardDiffsTheCheckout: no worktree, no branch — the diff
// family reads the managed checkout itself, and its base is the checkout's
// HEAD, so the loose tracked work is what the diff describes. An untracked
// file is not in it: in the person's own checkout it is theirs, not the
// card's (withUntracked).
func TestAMainCheckoutCardDiffsTheCheckout(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "tracked.txt", "original\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "tracked")
	writeFile(t, root, "tracked.txt", "changed\n")
	writeFile(t, root, "untracked.txt", "loose\n")
	m := newManager(t, root)
	f := mainCheckoutFeature(1)

	if ok, err := m.Exists(ctx, f); err != nil || ok {
		t.Fatalf("Exists = %v, %v; want no worktree", ok, err)
	}
	diff, err := m.Diff(ctx, f)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+changed") || !strings.Contains(diff, "-original") {
		t.Errorf("the tracked change is not the diff:\n%s", diff)
	}
	if strings.Contains(diff, "untracked.txt") {
		t.Error("an untracked file rode into the diff")
	}
	stat, err := m.DiffStat(ctx, f)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !strings.Contains(stat, "tracked.txt") {
		t.Errorf("DiffStat = %q, want it to name tracked.txt", stat)
	}
	base, err := m.DiffBase(ctx, f)
	if err != nil {
		t.Fatalf("DiffBase: %v", err)
	}
	head, err := runGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if base != head {
		t.Errorf("DiffBase = %q, want the checkout's HEAD %q", base, head)
	}
}

// TestAMainCheckoutCardHeadIsTheCheckouts: a card with no branch of its own
// tips at the checkout's HEAD, which is what a successor branch (a spec
// written from the session) forks from.
func TestAMainCheckoutCardHeadIsTheCheckouts(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := mainCheckoutFeature(2)

	head, err := m.Head(ctx, f)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	want, err := runGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if head != want {
		t.Errorf("Head = %q, want the checkout's HEAD %q", head, want)
	}
}

// TestAMainCheckoutCardIsNotRequiredAWorktree: the manager's ensure path
// refuses rather than cut the tree the card was minted to do without —
// the engine routes such a card away from Ensure, and this is the floor
// under that routing.
func TestAMainCheckoutCardIsNotRequiredAWorktree(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := mainCheckoutFeature(3)

	if _, err := m.Ensure(ctx, f); err == nil {
		t.Fatal("Ensure created a worktree for a main-checkout card")
	}
	if ok, _ := m.Exists(ctx, f); ok {
		t.Error("a worktree exists anyway")
	}
}
