package worktree

import "testing"

// TestLandedTrueForCardThatNeverMerged is BG-036's exact reproduction: a
// card's branch carries its own commit that was never merged or
// squash-merged, but main independently gains the identical content
// through a wholly unrelated commit — standing in for a sibling card
// landing the same fix first, a cherry-pick, or a hand-applied identical
// change. Before the fix, squashLanded's tree-equality check couldn't
// tell that apart from a real squash merge and reported the untouched
// branch as landed. Landed must read false: without a recorded landed
// commit for this branch, there is no lineage to test.
func TestLandedTrueForCardThatNeverMerged(t *testing.T) {
	root := newRepo(t)
	m := newManager(t, root)
	f := feature(2, "Warn when a profile is applied across projects")
	p, err := m.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "identical.txt", "same fix\n")
	mustGit(t, p, "add", ".")
	mustGit(t, p, "commit", "-q", "-m", "feature commit")

	// main independently gains the identical content via an unrelated
	// commit — the branch above is never touched, merged, or referenced.
	writeFile(t, root, "identical.txt", "same fix\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-q", "-m", "a sibling card landed the same fix first")

	landed, err := m.Landed(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if landed {
		t.Fatalf("BG-036: Landed()=%v for a card whose branch was never merged or squash-merged, want false", landed)
	}
}
