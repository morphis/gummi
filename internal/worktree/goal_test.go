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

// homeTree is the goal's tree in its home repo — the one every
// single-repo goal has and the only one these fixtures cut.
func homeTree(t *testing.T, p *Pool, g *domain.Feature) GoalTree {
	t.Helper()
	trees, err := p.GoalTrees(ctx, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trees {
		if tr.Home {
			return tr
		}
	}
	t.Fatal("the goal has no home tree")
	return GoalTree{}
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
	home := homeTree(t, pool, g)
	behind, err := home.Behind(ctx)
	if err != nil || !behind {
		t.Fatalf("goal behind main = %v, %v", behind, err)
	}
	merged, err := home.CatchUp(ctx, false)
	if err != nil || !merged {
		t.Fatalf("catch up = %v, %v", merged, err)
	}
	if err := cm.AssertNoForkDrift(ctx, c); err != nil {
		t.Fatalf("a catch-up merge must not drift a goal card: %v", err)
	}
	if merged, err := home.CatchUp(ctx, false); err != nil || merged {
		t.Fatalf("a caught-up goal has nothing to merge: %v, %v", merged, err)
	}

	// the goal lands on main as one merge commit over the card commits
	msha, err := home.Merge(ctx, "Merge GL-001: offline export")
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
	goalDir := filepath.Join(root, g.WorktreePath())
	commitIn(t, goalDir, "README.md", "goal side\n", "goal edits readme")
	commitIn(t, root, "README.md", "main side\n", "main edits readme")

	home := homeTree(t, pool, g)
	_, err := home.CatchUp(ctx, false)
	var cErr *CatchUpConflictError
	if !errors.As(err, &cErr) || cErr.Open || len(cErr.Files) != 1 {
		t.Fatalf("want an aborted conflict naming README.md, got %v", err)
	}
	if open, _ := home.MergeInProgress(ctx); open {
		t.Fatal("an aborted catch-up leaves no merge open")
	}

	_, err = home.CatchUp(ctx, true)
	if !errors.As(err, &cErr) || !cErr.Open {
		t.Fatalf("want an open conflict, got %v", err)
	}
	if err := home.ConcludeMerge(ctx); !errors.As(err, &cErr) {
		t.Fatalf("concluding with unmerged paths must refuse, got %v", err)
	}
	writeFile(t, goalDir, "README.md", "both sides\n")
	mustGit(t, goalDir, "add", "README.md")
	if err := home.ConcludeMerge(ctx); err != nil {
		t.Fatal(err)
	}
	if behind, _ := home.Behind(ctx); behind {
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

func TestWithMainCheckoutIsThrowaway(t *testing.T) {
	pool, _, g := goalPool(t)
	m, err := pool.ManagerFor(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	err = m.WithMainCheckout(context.Background(), func(dir string) error {
		seen = dir
		_, err := os.Stat(filepath.Join(dir, "README.md"))
		return err
	})
	if err != nil {
		t.Fatalf("main checkout: %v", err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("the checkout is removed afterwards: %v", err)
	}
	out, _ := runGit(context.Background(), m.repo, "worktree", "list")
	if strings.Contains(out, seen) {
		t.Fatalf("the checkout is pruned from git's list:\n%s", out)
	}
}

// TestGoalSpansRepositories: a goal whose cards are in two repositories
// gets a goal branch in each, a card forks from and lands on the one in
// ITS repository, and the goal lands once per repository.
func TestGoalSpansRepositories(t *testing.T) {
	pool, ws, repoA, repoB := twoRepoPool(t)
	g := goalFeature(1, "offline export")
	g.Repo = "a" // its home, settled from its cards by the plan gate
	pool.SetGoalLookup(func(_ context.Context, id domain.FeatureID) (domain.Feature, error) {
		return *g, nil
	})
	for _, repo := range []string{"a", "b"} {
		if _, err := pool.EnsureGoalTree(ctx, g, repo); err != nil {
			t.Fatalf("goal tree in %s: %v", repo, err)
		}
	}
	homeDir := filepath.Join(ws, ".gummi", "worktrees", string(g.ID))
	awayDir := filepath.Join(ws, ".gummi", "worktrees", string(g.ID)+"@b")
	for _, dir := range []string{homeDir, awayDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("goal tree %s: %v", dir, err)
		}
	}
	// each tree is a checkout of its own repository, on the same branch
	for dir, repo := range map[string]string{homeDir: repoA, awayDir: repoB} {
		if got := strings.TrimSpace(mustGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")); got != g.BranchName() {
			t.Fatalf("%s is on %q, want %q", dir, got, g.BranchName())
		}
		if !strings.Contains(mustGit(t, dir, "rev-parse", "--git-common-dir"), repo) {
			t.Fatalf("%s is not a worktree of %s", dir, repo)
		}
	}

	// a card in b resolves to the goal tree in b, not the one in a
	c := feature(2, "download button")
	c.Repo, c.GoalID = "b", g.ID
	cm, err := pool.ManagerFor(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if cm.RepoRoot() != awayDir {
		t.Fatalf("a card of repo b resolves to %q, want the goal tree in b (%q)", cm.RepoRoot(), awayDir)
	}
	cDir, err := cm.Create(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	commitIn(t, cDir, "button.ts", "click\n", "feat(ui): a download button")
	if _, err := cm.SquashMerge(ctx, c, "feat(ui): a download button"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustGit(t, awayDir, "log", "--format=%s"), "download button") {
		t.Fatal("a card of repo b must land on the goal branch in b")
	}
	if strings.Contains(mustGit(t, homeDir, "log", "--format=%s"), "download button") {
		t.Fatal("a card of repo b must not touch the goal branch in a")
	}

	// the home repo's goal branch has a landing of its own
	commitIn(t, homeDir, "export.go", "tar\n", "feat(api): export endpoint")

	trees, err := pool.GoalTrees(ctx, g, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 || !trees[0].Home || trees[0].Repo != "a" || trees[1].Repo != "b" {
		t.Fatalf("trees = %+v, want the home repo first", trees)
	}
	for _, tr := range trees {
		if landed, err := tr.Landed(ctx); err != nil || landed {
			t.Fatalf("%s landed before the merge = %v, %v", tr.Repo, landed, err)
		}
		if _, err := tr.Merge(ctx, "Merge GL-001: offline export"); err != nil {
			t.Fatalf("merging %s: %v", tr.Repo, err)
		}
		if landed, err := tr.Landed(ctx); err != nil || !landed {
			t.Fatalf("%s landed after the merge = %v, %v", tr.Repo, landed, err)
		}
	}
	if !strings.Contains(mustGit(t, repoA, "log", "--format=%s"), "export endpoint") {
		t.Fatal("the goal must land in a")
	}
	if !strings.Contains(mustGit(t, repoB, "log", "--format=%s"), "download button") {
		t.Fatal("the goal must land in b")
	}
	// only the home repo's merge is the goal card's landed commit
	if got, _ := pool.fs.LandedSHA(ctx, g.ID); got != strings.TrimSpace(mustGit(t, repoA, "rev-parse", "HEAD")) {
		t.Fatalf("landed sha = %q, want the home repo's merge", got)
	}
}

// TestGoalTreesFindsARepoNoCardIsInAnyMore: a repository whose cards have
// all landed still has their commits on its goal branch, so its tree is
// found from disk rather than from the card list.
func TestGoalTreesFindsARepoNoCardIsInAnyMore(t *testing.T) {
	pool, _, _, _ := twoRepoPool(t)
	g := goalFeature(1, "offline export")
	g.Repo = "a"
	for _, repo := range []string{"a", "b"} {
		if _, err := pool.EnsureGoalTree(ctx, g, repo); err != nil {
			t.Fatal(err)
		}
	}
	trees, err := pool.GoalTrees(ctx, g, nil) // no cards named at all
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 || trees[1].Repo != "b" {
		t.Fatalf("trees = %+v, want the tree in b found on disk", trees)
	}
}

// TestRemovingAGoalTakesEveryTreeItCut: cleanup of a goal is cleanup of
// all of it — a tree in a repository whose name the card never carried
// would otherwise sit checked out forever.
func TestRemovingAGoalTakesEveryTreeItCut(t *testing.T) {
	pool, ws, repoA, repoB := twoRepoPool(t)
	g := goalFeature(1, "offline export")
	g.Repo = "a"
	for _, repo := range []string{"a", "b"} {
		if _, err := pool.EnsureGoalTree(ctx, g, repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.Remove(ctx, g, true); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(ws, ".gummi", "worktrees", string(g.ID)),
		filepath.Join(ws, ".gummi", "worktrees", string(g.ID)+"@b"),
	} {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("%s survived the cleanup", dir)
		}
	}
	// the branch in b is gone with its tree: it carried nothing b's main
	// does not have
	if out := mustGit(t, repoB, "branch", "--list", g.BranchName()); strings.TrimSpace(out) != "" {
		t.Fatalf("b still has %q", out)
	}
	// the home repo's branch is the goal card's own, and outlives its
	// worktree the way every card's does
	if out := mustGit(t, repoA, "branch", "--list", g.BranchName()); strings.TrimSpace(out) == "" {
		t.Fatal("the goal card's own branch must survive its worktree")
	}
}

// TestAGoalTreeHoldingWorkSurvivesCleanup: a sibling tree whose branch
// carries commits nothing else has keeps its branch — a cleanup discards
// checkouts, never work.
func TestAGoalTreeHoldingWorkSurvivesCleanup(t *testing.T) {
	pool, ws, _, repoB := twoRepoPool(t)
	g := goalFeature(1, "offline export")
	g.Repo = "a"
	for _, repo := range []string{"a", "b"} {
		if _, err := pool.EnsureGoalTree(ctx, g, repo); err != nil {
			t.Fatal(err)
		}
	}
	away := filepath.Join(ws, ".gummi", "worktrees", string(g.ID)+"@b")
	commitIn(t, away, "unlanded.txt", "mine\n", "feat: a card that never landed")

	if err := pool.Remove(ctx, g, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(away); err == nil {
		t.Fatal("the tree is removed either way")
	}
	if out := mustGit(t, repoB, "branch", "--list", g.BranchName()); strings.TrimSpace(out) == "" {
		t.Fatal("a branch with unlanded work must survive the cleanup")
	}
}

// A repository whose own test suite regenerates a tracked file leaves the
// goal tree dirty, and the goal tree is what every card lands on — so the
// first landing after a check run refused, naming a "main checkout" the
// person would look for in the wrong place. RestoreTracked is what the
// conductor runs before it lands, so the side effect of running the
// repo's commands does not end the goal.
func TestACheckRunLeavingTheGoalTreeDirtyDoesNotBlockLandingForever(t *testing.T) {
	pool, root, g := goalPool(t)
	goalDir := filepath.Join(root, g.WorktreePath())
	commitIn(t, goalDir, "docs/generated.md", "ran on /bin/echo\n", "generated docs")

	c := feature(2, "local cache")
	c.GoalID = g.ID
	cm, err := pool.ManagerFor(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	cDir, err := cm.Create(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	commitIn(t, cDir, "cache.go", "package cache\n", "the card's work")

	// the goal's own checks run in the goal tree and the suite rewrites a
	// tracked file there
	writeFile(t, goalDir, "docs/generated.md", "ran on /usr/bin/echo\n")

	_, err = cm.SquashMerge(ctx, c, "feat(cache): local cache")
	if err == nil {
		t.Fatal("a dirty goal tree must still refuse a squash merge")
	}
	if !strings.Contains(err.Error(), "docs/generated.md") || !strings.Contains(err.Error(), goalDir) {
		t.Fatalf("the refusal must name what is modified and where: %v", err)
	}

	restored, err := RestoreTracked(ctx, goalDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0] != "docs/generated.md" {
		t.Fatalf("restored = %v", restored)
	}
	if _, err := RestoreTracked(ctx, goalDir); err != nil {
		t.Fatalf("a clean tree restores to nothing: %v", err)
	}
	if _, err := cm.SquashMerge(ctx, c, "feat(cache): local cache"); err != nil {
		t.Fatalf("landing after the tree was put back: %v", err)
	}
	if got := mustGit(t, goalDir, "log", "-1", "--format=%s"); got != "feat(cache): local cache" {
		t.Fatalf("goal branch tip = %q", got)
	}
}

// A half-finished merge in the goal tree has an owner; it is not a check
// run's leftovers and must not be swept away.
func TestRestoreTrackedLeavesAMergeInProgressAlone(t *testing.T) {
	_, root, g := goalPool(t)
	goalDir := filepath.Join(root, g.WorktreePath())
	commitIn(t, goalDir, "shared.txt", "goal side\n", "goal edit")
	commitIn(t, root, "shared.txt", "main side\n", "main edit")
	// merging main in conflicts and leaves the merge open
	_, _ = runGit(ctx, goalDir, "merge", "main")
	if _, err := RestoreTracked(ctx, goalDir); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("restore over an open merge = %v", err)
	}
	if got := mustGit(t, goalDir, "status", "--porcelain"); got == "" {
		t.Fatal("the open merge must be left exactly as it was")
	}
}
