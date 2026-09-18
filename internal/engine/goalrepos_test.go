package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// twoRepoGoalDoc is a goal whose work is in two repositories: one card in
// api, two in web. The home the plan gate settles on is web.
const twoRepoGoalDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: the cache file exists\n  repo: api\n  check: test -f cache.txt\n" +
	"- id: DW-2\n  says: the button offers the export\n  judge: true\n```\n\n" +
	"## Limits\n\nNo new dependencies.\n\n" +
	"## Budget\n\nAbout 1500 credits.\n\n```gummi-goal\nlanes: 1\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: local cache for export\n  repo: api\n  serves: [DW-1]\n  envelope: 600\n" +
	"- title: download button\n  repo: web\n  serves: [DW-2]\n  envelope: 400\n" +
	"- title: document the cache\n  repo: web\n  serves: [DW-2]\n  envelope: 200\n```\n\n" +
	"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n" +
	"## Verification plan\n\nRun the checks.\n\n## Report\n\n\n"

// twoRepoGoalEngine builds an engine over a `repos:`-only workspace with
// two named repositories and a goal at its plan gate, minted into the
// provisional home a creation surface would have given it.
func twoRepoGoalEngine(t *testing.T, doc string) (*Engine, *state.Store, *worktree.Pool, string, domain.Feature) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "web"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "config", "user.name", "t")
		gitIn(t, dir, "config", "user.email", "t@e.invalid")
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "-q", "-m", "init")
	}
	// a repos:-only workspace: the root is a mere parent of checkouts,
	// with no default repository of its own
	ws, err := state.Init(root, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	pool, err := worktree.NewPool(ctx, root, "", []worktree.NamedRepo{
		{Name: "api", Root: filepath.Join(root, "api")},
		{Name: "web", Root: filepath.Join(root, "web")},
	}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{Agents: singleAgent(agent.NewFake("")), Store: store, Pool: pool, Workspace: ws, Model: "fake-model"})
	t.Cleanup(func() { e.Close() })

	// the provisional home a creation surface gives a goal: nobody was
	// asked, and a repos:-only workspace has no default to fall back on
	if got := e.ProvisionalRepo(); got != "api" {
		t.Fatalf("provisional repo = %q, want the first configured one", got)
	}
	id, _ := domain.NewID(domain.KindGoal, 1)
	now := time.Now()
	g := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindGoal, Title: "Export works offline", Slug: "export-works-offline",
		Stage: domain.StagePlan, Repo: e.ProvisionalRepo(), Budget: domain.Budget{Envelope: 4000},
		CreatedAt: now, UpdatedAt: now,
	}
	putFeature(t, store, g)
	if err := os.WriteFile(filepath.Join(root, ".gummi", "seq"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.EnsureGoalTree(ctx, &g, g.Repo); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, g.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return e, store, pool, root, g
}

// TestGoalPlanGateTakesCardsInAnyConfiguredRepository: the gate's repo
// rule is that a row names a repository this workspace manages — not that
// it names the goal's own, which is the thing a goal across repositories
// cannot do.
func TestGoalPlanGateTakesCardsInAnyConfiguredRepository(t *testing.T) {
	e, _, _, _, g := twoRepoGoalEngine(t, twoRepoGoalDoc)
	if problem := e.goalPlanProblems(context.Background(), g); problem != "" {
		t.Fatalf("a plan spanning api and web is fit to start: %q", problem)
	}
}

// TestGoalPlanGateRefusesAnUnconfiguredRepository: and it says which
// repositories there are, since the row's author picked from a set.
func TestGoalPlanGateRefusesAnUnconfiguredRepository(t *testing.T) {
	doc := strings.Replace(twoRepoGoalDoc, "  repo: web\n  serves: [DW-2]\n  envelope: 400", "  repo: wbe\n  serves: [DW-2]\n  envelope: 400", 1)
	e, _, _, _, g := twoRepoGoalEngine(t, doc)
	problem := e.goalPlanProblems(context.Background(), g)
	if !strings.Contains(problem, `"wbe"`) || !strings.Contains(problem, "api, web") {
		t.Fatalf("problem = %q, want the bad name and the configured set", problem)
	}
}

// TestGoalPlanGateRefusesACheckInAnUnconfiguredRepository: a done-when
// check names where it runs, and nowhere is not an answer either.
func TestGoalPlanGateRefusesACheckInAnUnconfiguredRepository(t *testing.T) {
	doc := strings.Replace(twoRepoGoalDoc, "  repo: api\n  check: test -f cache.txt", "  repo: nope\n  check: test -f cache.txt", 1)
	e, _, _, _, g := twoRepoGoalEngine(t, doc)
	if problem := e.goalPlanProblems(context.Background(), g); !strings.Contains(problem, "DW-1's check") {
		t.Fatalf("problem = %q, want the item named", problem)
	}
}

// TestStartGoalSettlesItsHomeAndCutsOneTreePerRepository: crossing the
// plan gate moves the goal to the repository most of its cards are in,
// drops the provisional branch it was minted with, and gives it a branch
// in every repository its cards are actually in.
func TestStartGoalSettlesItsHomeAndCutsOneTreePerRepository(t *testing.T) {
	e, store, pool, root, g := twoRepoGoalEngine(t, twoRepoGoalDoc)
	ctx := context.Background()
	res, err := e.Advance(ctx, g.ID, "user")
	if err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	goal, _ := store.GetFeature(ctx, g.ID)
	if goal.Repo != "web" {
		t.Fatalf("home repo = %q, want web — two of its three cards are there", goal.Repo)
	}
	// the home tree is the goal card's own worktree; the other repo's is
	// its sibling
	home := filepath.Join(root, ".gummi", "worktrees", string(g.ID))
	away := filepath.Join(root, ".gummi", "worktrees", string(g.ID)+"@api")
	for _, dir := range []string{home, away} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("goal tree %s: %v", dir, err)
		}
	}
	// the goal card's own tree is a checkout of its settled home, not of
	// the repository it was provisionally minted into
	if common := gitOut(t, home, "rev-parse", "--git-common-dir"); !strings.Contains(common, filepath.Join(root, "web")) {
		t.Fatalf("the home tree is a worktree of %q, want web", strings.TrimSpace(common))
	}
	if common := gitOut(t, away, "rev-parse", "--git-common-dir"); !strings.Contains(common, filepath.Join(root, "api")) {
		t.Fatalf("the api tree is a worktree of %q, want api", strings.TrimSpace(common))
	}
	// and both repositories carry a branch of the goal's name — one each,
	// for the cards that are in them
	for _, repo := range []string{"api", "web"} {
		m, err := pool.ManagerForName(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := m.BranchExists(ctx, &goal); err != nil || !ok {
			t.Fatalf("%s has the goal branch = %v, %v", repo, ok, err)
		}
	}

	byTitle := map[string]domain.Feature{}
	for _, c := range goalCards(t, store, g.ID) {
		byTitle[c.Title] = c
	}
	for title, want := range map[string]string{
		"local cache for export": "api",
		"download button":        "web",
		"document the cache":     "web",
	} {
		c, ok := byTitle[title]
		if !ok {
			t.Fatalf("card %q was not minted: %v", title, byTitle)
		}
		if c.Repo != want {
			t.Fatalf("%s is in %q, want %q", c.ID, c.Repo, want)
		}
	}

	// a card of api forks from the goal branch in api, not the one in web
	card := byTitle["local cache for export"]
	m, err := pool.ManagerFor(ctx, &card)
	if err != nil {
		t.Fatal(err)
	}
	if m.RepoRoot() != away {
		t.Fatalf("the api card resolves to %q, want the goal tree in api (%q)", m.RepoRoot(), away)
	}
}

// TestLandGoalLandsOncePerRepository: git has no merge across
// repositories, so a goal in two of them lands twice — and the hand-over
// says which have it.
func TestLandGoalLandsOncePerRepository(t *testing.T) {
	e, store, _, root, g := twoRepoGoalEngine(t, twoRepoGoalDoc)
	ctx := context.Background()
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	if _, err := store.Transition(ctx, g.ID, domain.StageVerify, "user"); err != nil {
		t.Fatal(err)
	}
	// stand in for the cards' landings: a commit on each goal branch
	commitInTree(t, filepath.Join(root, ".gummi", "worktrees", string(g.ID)), "button.ts", "feat(ui): a download button")
	commitInTree(t, filepath.Join(root, ".gummi", "worktrees", string(g.ID)+"@api"), "cache.go", "feat(api): a local cache")

	sha, err := e.LandGoal(ctx, g.ID, "Merge GL-001: export works offline", "user")
	if err != nil {
		t.Fatalf("land: %v", err)
	}
	web := gitOut(t, filepath.Join(root, "web"), "log", "--format=%s")
	api := gitOut(t, filepath.Join(root, "api"), "log", "--format=%s")
	if !strings.Contains(web, "download button") || !strings.Contains(api, "local cache") {
		t.Fatalf("the goal must land in both repositories:\nweb: %s\napi: %s", web, api)
	}
	head := gitOut(t, filepath.Join(root, "web"), "rev-parse", "HEAD")
	if sha != strings.TrimSpace(head) {
		t.Fatalf("landed sha = %q, want the home repo's merge %q", sha, strings.TrimSpace(head))
	}
	got, _ := store.GetFeature(ctx, g.ID)
	if got.Stage != domain.StageDone {
		t.Fatalf("stage = %s, want done", got.Stage)
	}
	r, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Repos) != 2 {
		t.Fatalf("report repos = %+v", r.Repos)
	}
	for _, rp := range r.Repos {
		if !rp.Landed {
			t.Fatalf("%s is landed in the hand-over: %+v", rp.Name, rp)
		}
		if (rp.Name == "web") != rp.Home {
			t.Fatalf("home flag on %+v", rp)
		}
	}
}

// TestGoalChecksRunInTheRepositoryTheirItemNames: a done-when check is
// proved where its item says, not where the goal happens to live.
func TestGoalChecksRunInTheRepositoryTheirItemNames(t *testing.T) {
	e, store, _, root, g := twoRepoGoalEngine(t, twoRepoGoalDoc)
	ctx := context.Background()
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	goal, _ := store.GetFeature(ctx, g.ID)
	// DW-1's check is `test -f cache.txt`, and its item names api: the
	// file exists only in the goal's tree there
	commitInTree(t, filepath.Join(root, ".gummi", "worktrees", string(g.ID)+"@api"), "cache.txt", "feat(api): a local cache")

	doc, err := os.ReadFile(filepath.Join(root, goal.ArtifactPath()))
	if err != nil {
		t.Fatal(err)
	}
	checks := withDoneWhenChecks(string(doc), nil)
	results, err := e.runGoalChecks(ctx, goal, string(doc), checks)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "done-when DW-1" {
		t.Fatalf("results = %+v", results)
	}
	if !results[0].OK {
		t.Fatalf("DW-1's check must run in api, where its file is: %+v", results[0])
	}
}

// commitInTree commits a file into a checkout.
func commitInTree(t *testing.T, dir, file, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", msg)
}
