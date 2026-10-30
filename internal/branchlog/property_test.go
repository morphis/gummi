package branchlog

// Property tests of turning a person's squash choices into a history
// rewrite, driven by hegel (https://hegel.dev). worktree.RewritePlan
// promises no reordering and no dropping: every commit in exactly one
// group, so the branch's final tree cannot change.

import (
	"fmt"
	"slices"
	"testing"

	"hegel.dev/go/hegel"
)

// TestPropertyARewriteKeepsEveryCommitInOrder: whatever the reader folds
// and renames — the first commit included, SHAs that are not on the
// branch included — the groups hold every commit once, in order, one
// group for each commit not folded into the one before, and a group that
// folds several always carries a message.
func TestPropertyARewriteKeepsEveryCommitInOrder(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(0, 8))
		var rows []Row
		var shas []string
		squashed := map[string]bool{"not-on-branch": true}
		messages := map[string]string{"not-on-branch": "x"}
		want := 0
		for i := range n {
			sha := fmt.Sprintf("%040d", i)
			// git refuses an empty message unless told to: a commit has one
			subject := hegel.Draw(ht, hegel.SampledFrom([]string{"fix", "feat: x", " spaced "}))
			rows = append(rows, Row{SHA: sha, Subject: subject})
			shas = append(shas, sha)
			if fold := hegel.Draw(ht, hegel.Booleans()); fold {
				squashed[sha] = true
			}
			if hegel.Draw(ht, hegel.WeightedBooleans(0.3)) {
				messages[sha] = "renamed"
			}
			if i == 0 || !squashed[sha] {
				want++
			}
		}
		groups := PlanGroups(rows, squashed, messages)
		var got []string
		for _, g := range groups {
			if len(g.Commits) == 0 {
				ht.Fatalf("an empty group: %+v", groups)
			}
			got = append(got, g.Commits...)
			if len(g.Commits) > 1 && g.Message == "" {
				ht.Fatalf("a group folding %d commits has no message: %+v", len(g.Commits), groups)
			}
		}
		if !slices.Equal(got, shas) || len(groups) != want {
			ht.Fatalf("groups %+v hold %q in %d groups; want %q in %d", groups, got, len(groups), shas, want)
		}
	}, hegel.WithTestCases(2000), hegel.WithDatabase(""))
}
