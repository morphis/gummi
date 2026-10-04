package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// A goal lands what was checked: a card with a commit after its verify
// pass is not landed on the goal branch on the strength of that pass. Its
// checks run again on the new tip first, and a card whose checks cannot
// run waits for a person rather than landing work nothing has seen.
func TestAGoalDoesNotLandCommitsAddedAfterVerify(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("goal plan gate: %v %v", res, err)
	}
	cache := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID)
	verifyCard(t, e, store, root, cache.ID, "cache.txt")

	c, _ := store.GetFeature(ctx, cache.ID)
	m, err := e.WorktreesFor(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := m.Ensure(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unverified.txt"), []byte("u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "after verify")

	tick(t, e, g.ID)
	got, _ := store.GetFeature(ctx, cache.ID)
	if got.Stage == domain.StageDone || got.LandedSHA != "" {
		t.Fatalf("a commit after verify landed on the goal branch: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, g.WorktreePath(), "unverified.txt")); err == nil {
		t.Fatal("the unverified file is on the goal branch")
	}
}
