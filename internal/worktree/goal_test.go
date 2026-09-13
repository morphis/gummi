package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func goalFeature(num int, title string) *domain.Feature {
	id, _ := domain.NewID(domain.KindGoal, num)
	slug, _ := domain.Slugify(title)
	now := time.Now()
	return &domain.Feature{
		ID: id, Num: num, Kind: domain.KindGoal, Title: title, Slug: slug,
		Stage: domain.StageImplement, CreatedAt: now, UpdatedAt: now,
	}
}

// goalPool builds a pool over a fresh repo with a goal whose worktree
// exists, returning the pool, the repo root and the goal.
func goalPool(t *testing.T) (*Pool, string, *domain.Feature) {
	t.Helper()
	root := newRepo(t)
	pool, err := NewPool(ctx, root, root, nil, &memForkStore{}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := goalFeature(1, "offline export")
	main, err := pool.ManagerFor(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := main.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	return pool, root, g
}

func commitIn(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	writeFile(t, dir, file, content)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", msg)
}

func TestGoalCardsForkFromAndLandOnTheGoalBranch(t *testing.T) {
	pool, root, g := goalPool(t)
	mainMgr, _ := pool.ManagerFor(ctx, g)
	gMgr, err := pool.GoalManager(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if gMgr == mainMgr {
		t.Fatal("a goal's cards must resolve to a manager of their own")
	}
	goalDir := filepath.Join(root, g.WorktreePath())

	// the goal branch already carries a commit main does not
	commitIn(t, goalDir, "goal.txt", "goal\n", "goal scaffolding")

	c := feature(2, "local cache")
	c.GoalID = g.ID
	cm, err := pool.ManagerFor(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if cm != gMgr {
		t.Fatal("a goal card resolves to its goal's manager")
	}
	cDir, err := cm.Create(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cDir, "goal.txt")); err != nil {
		t.Fatalf("a goal card forks from the goal branch, not main: %v", err)
	}
	commitIn(t, cDir, "cache.go", "package cache\n", "wip 1")
	commitIn(t, cDir, "cache.go", "package cache // done\n", "wip 2")

	if ahead, err := cm.BranchAhead(ctx, c); err != nil || !ahead {
		t.Fatalf("card ahead of goal branch = %v, %v", ahead, err)
	}
	sha, err := cm.SquashMerge(ctx, c, "feat(cache): local cache")
	if err != nil {
		t.Fatal(err)
	}
	if landed, err := cm.Landed(ctx, c); err != nil || !landed {
		t.Fatalf("card landed on goal branch = %v, %v", landed, err)
	}
	if got := mustGit(t, goalDir, "log", "-1", "--format=%H %s"); got != sha+" feat(cache): local cache" {
		t.Fatalf("goal branch tip = %q", got)
	}
	if out := mustGit(t, root, "log", "--format=%s", "main"); strings.Contains(out, "local cache") {
		t.Fatal("landing a card on its goal must not touch main")
	}

	// main moves; the goal catches up with a merge and the card's fork
	// point stays an ancestor
	commitIn(t, root, "other.txt", "elsewhere\n", "main moves")
	behind, err := mainMgr.GoalBehindMain(ctx, g)
	if err != nil || !behind {
		t.Fatalf("goal behind main = %v, %v", behind, err)
	}
	merged, err := mainMgr.CatchUpGoal(ctx, g, false)
	if err != nil || !merged {
		t.Fatalf("catch up = %v, %v", merged, err)
	}
	if err := cm.AssertNoForkDrift(ctx, c); err != nil {
		t.Fatalf("a catch-up merge must not drift a goal card: %v", err)
	}
	if merged, err := mainMgr.CatchUpGoal(ctx, g, false); err != nil || merged {
		t.Fatalf("a caught-up goal has nothing to merge: %v, %v", merged, err)
	}

	// the goal lands on main as one merge commit over the card commits
	msha, err := mainMgr.MergeGoal(ctx, g, "Merge GL-001: offline export")
	if err != nil {
		t.Fatal(err)
	}
	if first := mustGit(t, root, "log", "--first-parent", "-1", "--format=%H %s"); first != msha+" Merge GL-001: offline export" {
		t.Fatalf("main first-parent tip = %q", first)
	}
	if !strings.Contains(mustGit(t, root, "log", "--format=%s"), "feat(cache): local cache") {
		t.Fatal("the card's own commit must survive under the goal merge")
	}
	if landed, err := mainMgr.Landed(ctx, g); err != nil || !landed {
		t.Fatalf("goal landed = %v, %v", landed, err)
	}
	stat, err := mainMgr.CommitStat(ctx, sha)
	if err != nil || !strings.Contains(stat, "cache.go") {
		t.Fatalf("commit stat = %q, %v", stat, err)
	}
}

func TestGoalCardWithoutWorktreeResolution(t *testing.T) {
	root := newRepo(t)
	pool, err := NewPool(ctx, root, root, nil, &memForkStore{}, false)
	if err != nil {
		t.Fatal(err)
	}
	c := feature(2, "card")
	c.GoalID = "GL-001"
	// no lookup: the repository manager
	if m, err := pool.ManagerFor(ctx, c); err != nil || m.RepoRoot() != root {
		t.Fatalf("fallback without lookup = %v, %v", m, err)
	}
	stage := domain.StageImplement
	pool.SetGoalLookup(func(_ context.Context, id domain.FeatureID) (domain.Feature, error) {
		return domain.Feature{ID: id, Kind: domain.KindGoal, Stage: stage}, nil
	})
	if _, err := pool.ManagerFor(ctx, c); !errors.Is(err, ErrGoalWorktreeMissing) {
		t.Fatalf("a running goal with no worktree must refuse, got %v", err)
	}
	stage = domain.StageDone
	if m, err := pool.ManagerFor(ctx, c); err != nil || m.RepoRoot() != root {
		t.Fatalf("an ended goal falls back to its repository: %v, %v", m, err)
	}
}

func TestCatchUpConflictAndConclude(t *testing.T) {
	pool, root, g := goalPool(t)
	mainMgr, _ := pool.ManagerFor(ctx, g)
	goalDir := filepath.Join(root, g.WorktreePath())
	commitIn(t, goalDir, "README.md", "goal side\n", "goal edits readme")
	commitIn(t, root, "README.md", "main side\n", "main edits readme")

	_, err := mainMgr.CatchUpGoal(ctx, g, false)
	var cErr *CatchUpConflictError
	if !errors.As(err, &cErr) || cErr.Open || len(cErr.Files) != 1 {
		t.Fatalf("want an aborted conflict naming README.md, got %v", err)
	}
	if open, _ := mainMgr.GoalMergeInProgress(ctx, g); open {
		t.Fatal("an aborted catch-up leaves no merge open")
	}

	_, err = mainMgr.CatchUpGoal(ctx, g, true)
	if !errors.As(err, &cErr) || !cErr.Open {
		t.Fatalf("want an open conflict, got %v", err)
	}
	if err := mainMgr.ConcludeGoalMerge(ctx, g); !errors.As(err, &cErr) {
		t.Fatalf("concluding with unmerged paths must refuse, got %v", err)
	}
	writeFile(t, goalDir, "README.md", "both sides\n")
	mustGit(t, goalDir, "add", "README.md")
	if err := mainMgr.ConcludeGoalMerge(ctx, g); err != nil {
		t.Fatal(err)
	}
	if behind, _ := mainMgr.GoalBehindMain(ctx, g); behind {
		t.Fatal("a concluded catch-up brings main in")
	}
}

func TestRebaseOntoMovesOnlyTheCardsCommits(t *testing.T) {
	pool, root, g := goalPool(t)
	mainMgr, _ := pool.ManagerFor(ctx, g)
	gMgr, _ := pool.GoalManager(ctx, g)
	goalDir := filepath.Join(root, g.WorktreePath())

	// an existing open-board card with work of its own
	c := feature(3, "existing")
	cDir, err := mainMgr.Create(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	commitIn(t, cDir, "mine.txt", "mine\n", "card work")
	oldFork, _ := mainMgr.ForkPoint(ctx, c)

	// the goal branch has another card's landing
	commitIn(t, goalDir, "other-card.txt", "other\n", "feat: other card")

	// attach: onto the goal branch
	c.GoalID = g.ID
	if err := gMgr.RebaseOnto(ctx, c, oldFork); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cDir, "other-card.txt")); err != nil {
		t.Fatal("an attached card sits on the goal branch")
	}
	if err := gMgr.AssertNoForkDrift(ctx, c); err != nil {
		t.Fatalf("an attached card is anchored on the goal branch: %v", err)
	}

	// detach: back onto main, without the other card's landing
	goalFork, _ := gMgr.ForkPoint(ctx, c)
	c.GoalID = ""
	if err := mainMgr.RebaseOnto(ctx, c, goalFork); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cDir, "other-card.txt")); err == nil {
		t.Fatal("a detached card must not carry other cards' landings back to main")
	}
	if _, err := os.Stat(filepath.Join(cDir, "mine.txt")); err != nil {
		t.Fatal("a detached card keeps its own work")
	}
	if err := mainMgr.AssertNoForkDrift(ctx, c); err != nil {
		t.Fatalf("a detached card is anchored on main: %v", err)
	}
}
