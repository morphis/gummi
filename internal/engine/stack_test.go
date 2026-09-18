package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// stackFixture builds a real repository with a three-card stack whose
// branches are actually cut and chained: A on main, B on A, C on B. Every
// git fact in these tests is git's, not a stub's — the whole point of the
// feature is what `rebase --onto` does, and a fake cannot tell us that.
type stackFixture struct {
	t     *testing.T
	root  string
	ws    state.Workspace
	store *state.Store
	eng   *Engine
	pool  *worktree.Pool
	cards map[domain.FeatureID]domain.Feature
}

func (f *stackFixture) git(args ...string) string {
	f.t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", f.root}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (f *stackFixture) gitIn(dir string, args ...string) string {
	f.t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}

// card mints a stacked card row directly (no agent involved) and returns
// it. pos is its place in the stack.
func (f *stackFixture) card(num int, title string, stackID domain.StackID, pos int) domain.Feature {
	f.t.Helper()
	c := feature(num, title, domain.StageImplement)
	c.BranchScheme = domain.BranchSchemeKind
	c.StackID, c.StackPos = stackID, pos
	if err := f.store.CreateFeature(context.Background(), &c); err != nil {
		f.t.Fatal(err)
	}
	f.cards[c.ID] = c
	return c
}

// cut creates the card's worktree and puts one commit of its own on the
// branch, so the branch has something for a replay to move.
func (f *stackFixture) cut(c domain.Feature, file, body string) {
	f.t.Helper()
	ctx := context.Background()
	mgr, err := f.pool.ManagerFor(ctx, &c)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := mgr.Create(ctx, &c)
	if err != nil {
		f.t.Fatalf("cutting %s: %v", c.ID, err)
	}
	if werr := os.WriteFile(filepath.Join(p, file), []byte(body), 0o600); werr != nil {
		f.t.Fatal(werr)
	}
	f.gitIn(p, "add", ".")
	f.gitIn(p, "commit", "-q", "-m", string(c.ID)+": work")
}

func newStackFixture(t *testing.T) *stackFixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &stackFixture{t: t, root: root, cards: map[domain.FeatureID]domain.Feature{}}
	f.git("init", "-q", "-b", "main")
	f.git("config", "user.name", "t")
	f.git("config", "user.email", "t@e.invalid")
	if werr := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	f.git("add", ".")
	f.git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	f.ws = ws
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f.store = store
	pool, err := worktree.NewPool(context.Background(), root, root, nil, store, false)
	if err != nil {
		t.Fatal(err)
	}
	f.pool = pool
	f.eng = New(Config{Store: store, Pool: pool, Workspace: ws})
	// The wiring main.go does at launch: without it a stacked card forks
	// from the checkout's HEAD instead of the card below it, which is
	// exactly the bug this seam exists to prevent.
	pool.SetBaseLookup(f.eng.StackBaseFor)

	st := domain.Stack{ID: "chain", Name: "chain"}
	if cerr := store.CreateStack(context.Background(), &st, time.Now()); cerr != nil {
		t.Fatal(cerr)
	}
	return f
}

// The core claim: a stacked card's branch is cut from the branch of the
// card below it, not from the checkout's HEAD.
func TestStackedBranchForksFromTheCardBelow(t *testing.T) {
	f := newStackFixture(t)
	a := f.card(1, "parser", "chain", 0)
	b := f.card(2, "eval", "chain", 1)
	f.cut(a, "a.txt", "a\n")
	f.cut(b, "b.txt", "b\n")

	// B's history must contain A's commit. If the base seam were not
	// wired, B would have been cut from main and this would fail.
	out := f.git("merge-base", "--is-ancestor", a.BranchName(), b.BranchName())
	_ = out
	if !f.contains(a.BranchName(), b.BranchName()) {
		t.Fatalf("%s is not in %s's history — the stacked card forked from the wrong base",
			a.BranchName(), b.BranchName())
	}
	// And B carries exactly its own commit beyond A.
	if n := f.countBetween(a.BranchName(), b.BranchName()); n != 1 {
		t.Errorf("%s has %d commits beyond %s, want 1", b.ID, n, a.BranchName())
	}
}

// contains reports whether rev is an ancestor of branch.
func (f *stackFixture) contains(rev, branch string) bool {
	f.t.Helper()
	err := exec.CommandContext(context.Background(), "git", "-C", f.root,
		"merge-base", "--is-ancestor", rev, branch).Run()
	return err == nil
}

// countBetween counts commits in from..to.
func (f *stackFixture) countBetween(from, to string) int {
	f.t.Helper()
	out := f.git("rev-list", "--count", from+".."+to)
	n := 0
	for _, ch := range out {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

// THE test for the whole feature: the card below changes (review
// feedback), and the tick replays the cards above it onto the new commits
// — carrying only their own work, never the card below's.
func TestStackTickReplaysAboveAChangedCard(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	a := f.card(1, "parser", "chain", 0)
	b := f.card(2, "eval", "chain", 1)
	c := f.card(3, "cli", "chain", 2)
	f.cut(a, "a.txt", "a\n")
	f.cut(b, "b.txt", "b\n")
	f.cut(c, "c.txt", "c\n")

	// Feedback on A: amend its commit, so everything above it is now
	// built on a commit that no longer exists.
	aTree := filepath.Join(f.root, ".gummi", "worktrees", string(a.ID))
	if werr := os.WriteFile(filepath.Join(aTree, "a.txt"), []byte("a fixed\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	f.gitIn(aTree, "commit", "-q", "-a", "--amend", "-m", "A: work, fixed")
	aTip := f.strip(f.git("rev-parse", a.BranchName()))

	// B is stale now and C is stale behind it.
	if f.contains(aTip, b.BranchName()) {
		t.Fatal("B should be stale after A was amended")
	}

	// Tick to a fixed point, as `stack restack` does.
	for i := 0; i < 16; i++ {
		res, err := f.eng.StackTick(ctx, "chain")
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.Conflict != nil {
			t.Fatalf("unexpected conflict on %s: %v", res.Restacked, res.Conflict.Files)
		}
		if !res.Again {
			break
		}
	}

	// Both cards above A now sit on A's new tip...
	if !f.contains(aTip, b.BranchName()) {
		t.Error("B was not replayed onto A's new commit")
	}
	bTip := f.strip(f.git("rev-parse", b.BranchName()))
	if !f.contains(bTip, c.BranchName()) {
		t.Error("C was not replayed onto B's new commit")
	}
	// ...and each still carries exactly its own one commit. This is the
	// property `--onto` buys and the one that matters most: a replay that
	// dragged the card below's commits along would make B "own" A's work.
	if n := f.countBetween(a.BranchName(), b.BranchName()); n != 1 {
		t.Errorf("B has %d commits beyond A after the replay, want 1", n)
	}
	if n := f.countBetween(b.BranchName(), c.BranchName()); n != 1 {
		t.Errorf("C has %d commits beyond B after the replay, want 1", n)
	}
	// The amended content is what B and C now sit on.
	body, err := os.ReadFile(filepath.Join(f.root, ".gummi", "worktrees", string(c.ID), "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "a fixed\n" {
		t.Errorf("C's tree has a.txt = %q, want the amended content", body)
	}
}

// strip trims trailing whitespace from git output.
func (f *stackFixture) strip(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// When the bottom card lands, the card above it stops forking from that
// branch and forks from the base directly — the landing cascade.
func TestStackBaseCollapsesWhenTheBottomLands(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	a := f.card(1, "parser", "chain", 0)
	b := f.card(2, "eval", "chain", 1)
	f.cut(a, "a.txt", "a\n")
	f.cut(b, "b.txt", "b\n")

	// Before landing, B forks from A's branch.
	got, err := f.eng.StackBaseFor(ctx, &b)
	if err != nil {
		t.Fatal(err)
	}
	if got != a.BranchName() {
		t.Fatalf("B's base = %q, want %q", got, a.BranchName())
	}

	// Land A the way gummi does, then re-read.
	mgr, err := f.pool.ManagerFor(ctx, &a)
	if err != nil {
		t.Fatal(err)
	}
	if _, merr := mgr.SquashMerge(ctx, &a, "A: landed"); merr != nil {
		t.Fatalf("landing A: %v", merr)
	}
	b2, err := f.store.GetFeature(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.eng.StackBaseFor(ctx, &b2)
	if err != nil {
		t.Fatal(err)
	}
	// A's own base was empty (the checkout's HEAD), so B inherits that:
	// its commits are in main now, and forking from its branch would be
	// re-applying work main already carries.
	if got != "" {
		t.Errorf("after A landed, B's base = %q, want the stack's base", got)
	}
}

// A card may not land while a card below it has not: its branch carries
// their commits, so landing it would land their work too.
func TestStackOrdersLandingOnly(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	a := f.card(1, "parser", "chain", 0)
	b := f.card(2, "eval", "chain", 1)
	f.cut(a, "a.txt", "a\n")
	f.cut(b, "b.txt", "b\n")

	if blocker, blocked := f.eng.StackLandBlocker(ctx, &b); !blocked || blocker != a.ID {
		t.Errorf("B's land blocker = %q,%v, want %s,true", blocker, blocked, a.ID)
	}
	if _, blocked := f.eng.StackLandBlocker(ctx, &a); blocked {
		t.Error("the bottom card must never be blocked from landing")
	}

	// And the ordering is ONLY about landing: both cards are at implement
	// and neither is held out of its stage by the other. Advance's
	// dependency gate is the thing that blocks work, and a stack writes
	// no dependency edges — assert that directly.
	deps, err := f.store.ListDependencies(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("stacking wrote %d dependency edge(s); a stack is topology, not scheduling", len(deps))
	}
}

// A conflict stops the walk where it happened: the conflicted card's
// branch is left untouched, and the cards below it stay correct.
func TestStackTickStopsOnConflictLeavingTheBranchIntact(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	a := f.card(1, "parser", "chain", 0)
	b := f.card(2, "eval", "chain", 1)
	f.cut(a, "shared.txt", "from A\n")
	f.cut(b, "shared.txt", "from B\n")

	aTree := filepath.Join(f.root, ".gummi", "worktrees", string(a.ID))
	if werr := os.WriteFile(filepath.Join(aTree, "shared.txt"), []byte("from A, revised\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	f.gitIn(aTree, "commit", "-q", "-a", "--amend", "-m", "A: revised")

	bTipBefore := f.strip(f.git("rev-parse", b.BranchName()))
	res, err := f.eng.StackTick(ctx, "chain")
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Conflict == nil {
		t.Fatal("want a conflict replaying B onto the revised A")
	}
	if res.Restacked != b.ID {
		t.Errorf("conflict reported on %s, want %s", res.Restacked, b.ID)
	}
	// The branch must be exactly where it was: RebaseOnto aborts before
	// returning, so a declined conflict never leaves a half-rebased
	// branch behind.
	if after := f.strip(f.git("rev-parse", b.BranchName())); after != bTipBefore {
		t.Errorf("B's branch moved to %s despite the conflict, want %s", after, bTipBefore)
	}
	if inProgress, _ := f.pool.RebaseInProgress(ctx, &b); inProgress {
		t.Error("a rebase was left in flight in B's worktree")
	}
}

// An unstacked card behaves exactly as it did before bases existed: it
// forks from the checkout's HEAD and reports no base of its own. This is
// the regression that makes the twenty-site baseRev sweep reviewable.
func TestUnstackedCardKeepsTheOldBehavior(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	c := feature(9, "plain", domain.StageImplement)
	if err := f.store.CreateFeature(ctx, &c); err != nil {
		t.Fatal(err)
	}
	base, err := f.eng.StackBaseFor(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	if base != "" {
		t.Errorf("an unstacked card's base = %q, want empty (the checkout's HEAD)", base)
	}
	f.cut(c, "p.txt", "p\n")
	// Cut from main, exactly as before.
	mainTip := f.strip(f.git("rev-parse", "main"))
	if !f.contains(mainTip, c.BranchName()) {
		t.Error("an unstacked card did not fork from the checkout's HEAD")
	}
	if _, blocked := f.eng.StackLandBlocker(ctx, &c); blocked {
		t.Error("an unstacked card must never be land-blocked")
	}
}

// A card with a chosen base forks from that branch even though the
// checkout has another one out — the selectable-base case, with no stack
// in sight.
func TestChosenBaseForksFromThatBranch(t *testing.T) {
	f := newStackFixture(t)
	ctx := context.Background()
	f.git("branch", "release-2.1")
	f.git("checkout", "-q", "release-2.1")
	if werr := os.WriteFile(filepath.Join(f.root, "rel.txt"), []byte("rel\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	f.git("add", ".")
	f.git("commit", "-q", "-m", "release-only commit")
	relTip := f.strip(f.git("rev-parse", "release-2.1"))
	f.git("checkout", "-q", "main")

	c := feature(9, "hotfix", domain.StageImplement)
	c.Base = "release-2.1"
	if err := f.store.CreateFeature(ctx, &c); err != nil {
		t.Fatal(err)
	}
	f.cut(c, "h.txt", "h\n")
	if !f.contains(relTip, c.BranchName()) {
		t.Error("a card with a chosen base did not fork from it")
	}
	// And it refuses to land while the checkout has a different branch
	// out, rather than squashing onto main.
	mgr, err := f.pool.ManagerFor(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	if _, merr := mgr.SquashMerge(ctx, &c, "hotfix"); merr == nil {
		t.Error("landing onto the wrong checked-out branch was allowed")
	}
}
