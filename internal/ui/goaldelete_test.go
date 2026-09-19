package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// goalWithCards puts a two-repo goal on the board of a repoWorkspace
// shell: a goal tree in "a" (its home) and in "b", and one card checked
// out inside each of them.
func goalWithCards(t *testing.T, m *Shell) (domain.Feature, []domain.Feature) {
	t.Helper()
	ctx := context.Background()
	goal := domain.Feature{
		ID: "GL-002", Num: 2, Kind: domain.KindGoal, Title: "Export works offline",
		Slug: "export-works-offline", Stage: domain.StageImplement, Repo: "a",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, &goal); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"a", "b"} {
		tree, err := m.wt.EnsureGoalTree(ctx, &goal, repo)
		if err != nil {
			t.Fatalf("goal tree in %q: %v", repo, err)
		}
		// a commit of its own on every goal branch: the delete is asked
		// for the branches git would refuse to drop, not only the ones
		// that happen to be identical to main.
		commitIn(t, tree.Dir, "goal-"+repo+".txt")
	}
	cards := []domain.Feature{
		{ID: "FD-010", Num: 10, Title: "local cache", Slug: "local-cache",
			Stage: domain.StageImplement, Repo: "a", GoalID: goal.ID,
			CreatedAt: fixedTime, UpdatedAt: fixedTime},
		{ID: "FD-011", Num: 11, Title: "offline flag", Slug: "offline-flag",
			Stage: domain.StageImplement, Repo: "b", GoalID: goal.ID,
			CreatedAt: fixedTime, UpdatedAt: fixedTime},
	}
	for i := range cards {
		if err := m.store.CreateFeature(ctx, &cards[i]); err != nil {
			t.Fatal(err)
		}
		dir, err := m.wt.Ensure(ctx, &cards[i])
		if err != nil {
			t.Fatalf("worktree for %s: %v", cards[i].ID, err)
		}
		commitIn(t, dir, string(cards[i].ID)+".txt")
	}
	return goal, cards
}

// commitIn puts one commit on whatever branch dir has checked out.
func commitIn(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", name}, {"commit", "-q", "-m", "work: " + name}} {
		out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

// branches lists repo's local branches.
func branches(t *testing.T, repo string) []string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		"-C", repo, "branch", "--format=%(refname:short)").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch in %s: %v\n%s", repo, err, out)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// TestDeletingAGoalTakesItsCardsWithIt: a goal's cards live inside its
// trees, so a delete that kept them would leave rows pointing at a goal
// that is gone, holding branches nothing would ever offer to remove and
// checkouts already deleted underneath them.
func TestDeletingAGoalTakesItsCardsWithIt(t *testing.T) {
	m := repoWorkspace(t)
	ctx := context.Background()
	goal, cards := goalWithCards(t, m)
	repoA := filepath.Join(m.wt.Root(), "git", "a")
	repoB := filepath.Join(m.wt.Root(), "git", "b")
	if len(branches(t, repoA)) != 3 { // main, the goal branch, one card
		t.Fatalf("precondition: branches in a = %v", branches(t, repoA))
	}

	msg, ok := m.deleteFeature(goal.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if !strings.Contains(msg.text, "with its 2 cards") ||
		!strings.Contains(msg.text, "FD-010, FD-011") {
		t.Errorf("the notice names what went with the goal: %q", msg.text)
	}

	for _, f := range append([]domain.Feature{goal}, cards...) {
		if _, err := m.store.GetFeature(ctx, f.ID); err == nil {
			t.Errorf("%s still has a record", f.ID)
		}
	}
	for repo, dir := range map[string]string{"a": repoA, "b": repoB} {
		if got := branches(t, dir); len(got) != 1 || got[0] != "main" {
			t.Errorf("branches left in %q: %v", repo, got)
		}
	}
	entries, err := os.ReadDir(filepath.Join(m.wt.Root(), ".gummi", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), string(goal.ID)) {
			t.Errorf("%s is still checked out", e.Name())
		}
	}
}

// TestDeletingAGoalIsRefusedWhileAnOutsiderDependsOnACard: the edge
// belongs to a card the delete does not own, so it is the reader's to
// break — and nothing is destroyed while it stands.
func TestDeletingAGoalIsRefusedWhileAnOutsiderDependsOnACard(t *testing.T) {
	m := repoWorkspace(t)
	ctx := context.Background()
	goal, cards := goalWithCards(t, m)
	if err := m.store.AddDependency(ctx, "FD-001", cards[0].ID); err != nil {
		t.Fatal(err)
	}

	msg, ok := m.deleteFeature(goal.ID)().(noticeMsg)
	if !ok || !msg.isErr {
		t.Fatalf("delete should refuse: %#v", msg)
	}
	if !strings.Contains(msg.text, "FD-001 depends on FD-010") {
		t.Errorf("the notice names the pair to break: %q", msg.text)
	}
	for _, f := range append([]domain.Feature{goal}, cards...) {
		if _, err := m.store.GetFeature(ctx, f.ID); err != nil {
			t.Errorf("%s was deleted anyway: %v", f.ID, err)
		}
	}
	deps, err := m.store.ListDependencies(ctx, "FD-001")
	if err != nil || len(deps) != 1 {
		t.Errorf("the refused delete kept the edge: %v %v", deps, err)
	}
	if ok, err := m.wt.BranchExists(ctx, &cards[0]); err != nil || !ok {
		t.Errorf("%s lost its branch to a refused delete: %v %v", cards[0].ID, ok, err)
	}
}

// TestDeletingAGoalWhoseTreesWereRemovedByHand: the cards' checkouts and
// branches are only reachable through the goal's trees, so the delete
// cuts them again — on the branches that are still there — rather than
// walking away from work it promised to remove.
func TestDeletingAGoalWhoseTreesWereRemovedByHand(t *testing.T) {
	m := repoWorkspace(t)
	ctx := context.Background()
	goal, cards := goalWithCards(t, m)
	for _, name := range []string{string(goal.ID), string(goal.ID) + "@b"} {
		if err := os.RemoveAll(filepath.Join(m.wt.Root(), ".gummi", "worktrees", name)); err != nil {
			t.Fatal(err)
		}
	}

	msg, ok := m.deleteFeature(goal.ID)().(noticeMsg)
	if !ok || msg.isErr {
		t.Fatalf("delete refused: %#v", msg)
	}
	if strings.Contains(msg.text, "no goal tree left to reach") {
		t.Errorf("a tree removed by hand is recut, not given up on: %q", msg.text)
	}
	for _, f := range append([]domain.Feature{goal}, cards...) {
		if _, err := m.store.GetFeature(ctx, f.ID); err == nil {
			t.Errorf("%s still has a record", f.ID)
		}
	}
	for repo, dir := range map[string]string{
		"a": filepath.Join(m.wt.Root(), "git", "a"),
		"b": filepath.Join(m.wt.Root(), "git", "b"),
	} {
		if got := branches(t, dir); len(got) != 1 || got[0] != "main" {
			t.Errorf("branches left in %q: %v", repo, got)
		}
	}
}
