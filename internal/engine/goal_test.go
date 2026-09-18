package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

const testGoalDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: the cache file exists\n  check: test -f cache.txt\n" +
	"- id: DW-2\n  says: the docs mention the cache\n  judge: true\n```\n\n" +
	"## Limits\n\nNo new dependencies.\n\n" +
	"## Budget\n\nAbout 1500 credits.\n\n```gummi-goal\nlanes: 1\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: local cache for export\n  serves: [DW-1]\n  envelope: 600\n" +
	"- title: document the cache\n  serves: [DW-2]\n  depends_on: [local cache for export]\n```\n\n" +
	"## Notes\n\n\n" +
	"## Try it\n\n\n" +
	"## Review\n\n\n" +
	"## Verification plan\n\nRun the checks.\n\n" +
	"## Report\n\n\n"

// goalAtPlan puts a goal at its plan gate: a worktree on its own branch
// and a filled goal doc at its workspace home.
func goalAtPlan(t *testing.T, store *state.Store, wt *worktree.Manager, doc string, envelope int) domain.Feature {
	t.Helper()
	id, _ := domain.NewID(domain.KindGoal, 1)
	now := time.Now()
	g := domain.Feature{ID: id, Num: 1, Kind: domain.KindGoal, Title: "Export works offline", Slug: "export-works-offline",
		Stage: domain.StagePlan, Budget: domain.Budget{Envelope: envelope}, CreatedAt: now, UpdatedAt: now}
	putFeature(t, store, g)
	// the seq counter must not hand GL-001's number to a minted card
	if err := os.WriteFile(filepath.Join(wt.Root(), ".gummi", "seq"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withWorktree(t, wt, g)
	writeArtifact(t, wt.Root(), g, doc)
	return g
}

func writeArtifact(t *testing.T, root string, f domain.Feature, content string) {
	t.Helper()
	p := filepath.Join(root, f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goalCards(t *testing.T, store *state.Store, goal domain.FeatureID) []domain.Feature {
	t.Helper()
	cards, err := store.ListGoalCards(context.Background(), goal)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func tick(t *testing.T, e *Engine, goal domain.FeatureID) GoalTickResult {
	t.Helper()
	res, err := e.GoalTick(context.Background(), goal)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return res
}

// verifyCard walks a goal card to a verified branch the way its autopilot
// would: a worktree forked from the goal branch, a commit, a drafted
// verification plan, and the verified stamp.
func verifyCard(t *testing.T, e *Engine, store *state.Store, root string, id domain.FeatureID, file string) {
	t.Helper()
	ctx := context.Background()
	c, err := store.GetFeature(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		if c.Stage == domain.StageDone {
			break
		}
		if c, err = store.Transition(ctx, id, st, "auto"); err != nil {
			t.Fatal(err)
		}
	}
	m, err := e.WorktreesFor(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := m.Ensure(ctx, &c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "work on "+string(id))
	art := spec.Template(&c)
	art, _, _ = spec.ReplaceSection(art, "Verification plan", "Checked by hand.\n")
	writeArtifact(t, root, c, art)
	if err := store.SetVerifiedAt(ctx, id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestGoalPlanGateRefusesUnrunnablePlans(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	cases := map[string]string{
		"no done-when": strings.Replace(testGoalDoc, "- id: DW-1\n  says: the cache file exists\n  check: test -f cache.txt\n- id: DW-2\n  says: the docs mention the cache\n  judge: true\n", "", 1),
		"unserved":     strings.Replace(testGoalDoc, "  serves: [DW-2]\n", "  serves: [DW-1]\n", 1),
		"no cards":     strings.Replace(testGoalDoc, "- title: local cache for export\n  serves: [DW-1]\n  envelope: 600\n- title: document the cache\n  serves: [DW-2]\n  depends_on: [local cache for export]\n", "", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			g := goalAtPlan(t, store, wt, doc, 4000)
			t.Cleanup(func() {
				_ = wt.Remove(ctx, &g, true)
				_ = wt.DeleteBranch(ctx, &g, true)
				_ = store.DeleteFeature(ctx, g.ID)
			})
			res, err := e.Advance(ctx, g.ID, "user")
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != StatusBlockedGoalPlan || res.Reason == "" {
				t.Fatalf("status = %v reason %q", res.Status, res.Reason)
			}
		})
	}

	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 250)
	res, err := e.Advance(ctx, g.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, "less than") {
		t.Fatalf("a budget that cannot fund the cards is refused: %v %q", res.Status, res.Reason)
	}
}

func TestGoalRunsItsCardsOnTheGoalBranch(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)

	res, err := e.Advance(ctx, g.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusAdvanced {
		t.Fatalf("goal plan gate: %v %q", res.Status, res.Reason)
	}

	// the crossing minted both cards into the goal, on autopilot, funded
	cards := goalCards(t, store, g.ID)
	if len(cards) != 2 {
		t.Fatalf("cards = %+v", cards)
	}
	cache, docs := cards[0], cards[1]
	if cache.GateMode() != domain.GateAutopilot || cache.Budget.Envelope != 600 {
		t.Fatalf("cache card = %+v", cache)
	}
	// pool = (4000 − 600 reserve) × 90% headroom = 3060, shared two ways.
	// The cache card asked for 600 and gets it; the docs card has no
	// estimate and starts at its share — NOT at the rest of the pool. The
	// difference stays unallocated, which is what the conductor raises
	// from when a card proves it needs more.
	if docs.Budget.Envelope != 1530 {
		t.Fatalf("docs card envelope = %d, want its share", docs.Budget.Envelope)
	}
	if view, verr := e.GoalView(ctx, g.ID); verr != nil {
		t.Fatal(verr)
	} else if view.Ledger.Available < 900 {
		t.Fatalf("minting leaves room to raise from, available = %.0f", view.Ledger.Available)
	}
	if deps, _ := store.ListDependencies(ctx, docs.ID); len(deps) != 1 || deps[0] != cache.ID {
		t.Fatalf("docs deps = %v", deps)
	}
	gotGoal, _ := store.GetFeature(ctx, g.ID)
	if gotGoal.GateMode() != domain.GateAutopilot || gotGoal.Goal.LaneCount() != 1 {
		t.Fatalf("goal after start = %+v", gotGoal)
	}
	raw, _ := os.ReadFile(filepath.Join(root, g.ArtifactPath()))
	rows, _, err := spec.ParseGoalCards(string(raw), []domain.DoneWhen{{ID: "DW-1"}, {ID: "DW-2"}})
	if err != nil || rows[0].ID != cache.ID || rows[1].ID != docs.ID {
		t.Fatalf("ids not written back: %+v %v", rows, err)
	}
	checks, _, _ := spec.ParseChecks(string(raw))
	if len(checks) != 1 || checks[0].Name != "done-when DW-1" || checks[0].Baseline == nil || *checks[0].Baseline {
		t.Fatalf("done-when checks = %+v", checks)
	}

	// first tick: only the card with no dependency starts
	r := tick(t, e, g.ID)
	if len(r.Start) != 1 || r.Start[0].ID != cache.ID {
		t.Fatalf("start = %+v (actions %v)", r.Start, r.Actions)
	}

	// the card verifies on its own branch, forked from the goal branch
	verifyCard(t, e, store, root, cache.ID, "cache.txt")
	r = tick(t, e, g.ID)
	if !r.Again {
		t.Fatalf("a landing asks for another tick: %v", r.Actions)
	}
	landed, _ := store.GetFeature(ctx, cache.ID)
	if landed.Stage != domain.StageDone || landed.LandedSHA == "" {
		t.Fatalf("cache card after landing = %+v", landed)
	}
	goalDir := filepath.Join(root, g.WorktreePath())
	if _, err := os.Stat(filepath.Join(goalDir, "cache.txt")); err != nil {
		t.Fatalf("the card's work is on the goal branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cache.txt")); err == nil {
		t.Fatal("landing a goal card must not touch main")
	}

	// the dependency landed, so the docs card starts
	r = tick(t, e, g.ID)
	if len(r.Start) != 1 || r.Start[0].ID != docs.ID {
		t.Fatalf("start = %+v (actions %v)", r.Start, r.Actions)
	}
	verifyCard(t, e, store, root, docs.ID, "CACHE.md")
	tick(t, e, g.ID)

	// everything landed: the goal finishes whole and its review starts
	r = tick(t, e, g.ID)
	if !r.Finished {
		t.Fatalf("goal should finish: %v", r.Actions)
	}
	gotGoal, _ = store.GetFeature(ctx, g.ID)
	if gotGoal.Goal.Partial != "" {
		t.Fatalf("a goal whose cards all landed is whole, got partial %q", gotGoal.Goal.Partial)
	}

	view, err := e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Cards) != 2 || view.Cards[0].State != goalpolicy.Landed || view.Cards[1].State != goalpolicy.Landed {
		t.Fatalf("view = %+v", view.Cards)
	}

	// the goal's own sessions leave scratch in its worktree (a binary built
	// for a check, captured stderr); none of it is committed or landed
	if err := os.WriteFile(filepath.Join(goalDir, "err.txt"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.checkpoint(&Session{Feature: gotGoal}); err != nil {
		t.Fatal(err)
	}

	// it lands on main as one merge commit over both card commits
	e.Drop(g.ID)
	if _, err := store.Transition(ctx, g.ID, domain.StageVerify, "auto"); err != nil {
		t.Fatal(err)
	}
	sha, err := e.LandGoal(ctx, g.ID, "", "user")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		t.Fatal("no merge sha")
	}
	if _, err := os.Stat(filepath.Join(root, "cache.txt")); err != nil {
		t.Fatalf("the goal's work is on main after landing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "err.txt")); err == nil {
		t.Fatal("scratch the goal's sessions left in its worktree must not land on main")
	}
	gotGoal, _ = store.GetFeature(ctx, g.ID)
	if gotGoal.Stage != domain.StageDone {
		t.Fatalf("goal stage = %s", gotGoal.Stage)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	var actions []string
	for _, en := range log {
		actions = append(actions, en.Action)
	}
	joined := strings.Join(actions, " ")
	for _, want := range []string{"minted minted", "started", "landed", "finished"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log %q lacks %q", joined, want)
		}
	}

	// The cards' checkouts and merged branches come out with the goal's,
	// and this is the only moment they can: a card of a goal resolves to a
	// manager rooted at the goal's tree, so once that tree is gone its
	// branch is measured against a trunk that never took its commits and
	// reads as unlanded for good.
	swept, err := e.CleanGoalCards(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(swept.Took) != 2 || len(swept.Left) != 0 {
		t.Fatalf("both landed cards come out with the goal: %+v", swept)
	}
	for _, c := range []domain.Feature{cache, docs} {
		if _, err := os.Stat(filepath.Join(goalDir, c.WorktreePath())); err == nil {
			t.Fatalf("%s is still checked out inside the goal tree", c.ID)
		}
		if ok, berr := wt.BranchExists(ctx, &c); berr != nil || ok {
			t.Fatalf("%s kept its branch: %v %v", c.ID, ok, berr)
		}
	}
	if _, err := os.Stat(goalDir); err != nil {
		t.Fatalf("the goal's own tree stays until the goal itself is cleaned: %v", err)
	}
}

func TestStopGoalDropsUnfinishedWorkAndFinishesPartial(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	tick(t, e, g.ID)
	if err := e.StopGoal(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	r := tick(t, e, g.ID)
	if !r.Finished {
		t.Fatalf("a stopped goal with nothing verified finishes: %v", r.Actions)
	}
	for _, c := range goalCards(t, store, g.ID) {
		if !c.GoalDropped() {
			t.Fatalf("%s should be dropped", c.ID)
		}
		// a card the goal made and dropped is closed, not left in flight —
		// and closed as DROPPED. It used to borrow the hand-off stamp to
		// clear Advance's landing floor, so every surface afterwards
		// reported a card nobody handed off as handed off.
		if c.Stage != domain.StageDone {
			t.Fatalf("%s is closed: stage %s", c.ID, c.Stage)
		}
		if c.HandedOff() {
			t.Fatalf("%s is dropped, not handed off: handed_off_at is stamped", c.ID)
		}
		if got := c.Ending(false); got != domain.EndingDropped {
			t.Fatalf("%s ending = %q, want dropped", c.ID, got)
		}
	}
	open, _ := store.OpenDecisions(ctx)
	for _, c := range goalCards(t, store, g.ID) {
		if len(open[c.ID]) > 0 {
			t.Fatalf("a closed card has nothing waiting: %+v", open[c.ID])
		}
	}
	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Cards {
		if c.State != "dropped" {
			t.Fatalf("the report still says why a closed card left the goal: %+v", c)
		}
	}
	got, _ := store.GetFeature(ctx, g.ID)
	if got.Goal.Partial != "you stopped the goal" {
		t.Fatalf("partial = %q", got.Goal.Partial)
	}
}

func TestGoalBudgetIsAHardCeiling(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	view, _ := e.GoalView(ctx, g.ID)
	cache := view.Cards[0]
	// 1270 is left to give: 4000 − (600 + 1530 started) − 600 reserve. The
	// pool holding more back than it used to does not soften the ceiling:
	// a raise past what is left is still refused.
	if err := e.goalRaise(ctx, view.Goal, cache, 2000, "test", "lead"); err == nil || !strings.Contains(err.Error(), "left to give") {
		t.Fatalf("a raise past the goal budget must be refused, got %v", err)
	}
	if err := e.RaiseGoalBudget(ctx, g.ID, 4500); err != nil {
		t.Fatal(err)
	}
	view, _ = e.GoalView(ctx, g.ID)
	if err := e.goalRaise(ctx, view.Goal, view.Cards[0], 2000, "test", "lead"); err != nil {
		t.Fatalf("with budget raised the card can be raised: %v", err)
	}
	if err := e.RaiseGoalBudget(ctx, g.ID, 100); err == nil {
		t.Fatal("lowering the ceiling is not a raise")
	}
}

func TestAttachedCardMovesOntoTheGoalAndBackWhenDropped(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()

	// an existing board card with work on its branch
	existing := feature(7, "flaky export test", domain.StageImplement)
	putFeature(t, store, existing)
	dir, err := wt.Create(ctx, &existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flaky.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "wip")
	if err := store.AddSpend(ctx, existing.ID, 120, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "  depends_on: [local cache for export]\n- title: flaky export test\n  id: FD-007\n  serves: [DW-1]\n", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	if err := os.WriteFile(filepath.Join(root, ".gummi", "seq"), []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the goal branch already carries a commit main does not
	goalDir := filepath.Join(root, g.WorktreePath())
	if err := os.WriteFile(filepath.Join(goalDir, "goal.txt"), []byte("g\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, goalDir, "add", "-A")
	gitIn(t, goalDir, "commit", "-q", "-m", "goal scaffolding")

	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	att, _ := store.GetFeature(ctx, existing.ID)
	if att.GoalID != g.ID || !att.GoalAttached || att.GateMode() != domain.GateAutopilot {
		t.Fatalf("attached card = %+v", att)
	}
	if _, err := os.Stat(filepath.Join(dir, "goal.txt")); err != nil {
		t.Fatal("an attached card with a branch moves onto the goal branch")
	}
	view, _ := e.GoalView(ctx, g.ID)
	gc, _ := view.Card(existing.ID)
	if gc.Spent != 0 || gc.Envelope != domain.MinEnvelope {
		t.Fatalf("pre-goal spend does not count against the goal: %+v", gc)
	}

	if err := e.goalDrop(ctx, view.Goal, att, "not needed", "lead"); err != nil {
		t.Fatal(err)
	}
	back, _ := store.GetFeature(ctx, existing.ID)
	if back.InGoal() || back.GoalDropped() {
		t.Fatalf("a dropped attached card returns to the board: %+v", back)
	}
	if _, err := os.Stat(filepath.Join(dir, "goal.txt")); err == nil {
		t.Fatal("a detached card must not carry the goal branch back")
	}
	if _, err := os.Stat(filepath.Join(dir, "flaky.txt")); err != nil {
		t.Fatal("a detached card keeps its own work")
	}
}

func TestGoalImplementRunIsConductedNotRun(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	cur, _ := store.GetFeature(ctx, g.ID)
	if err := e.RunWith(cur, "the review found a duplicated helper"); err != nil {
		t.Fatal(err)
	}
	if e.Get(g.ID) != nil {
		t.Fatal("a goal's implement stage never gets a stage session")
	}
	log, _ := store.GoalLog(ctx, g.ID)
	last := log[len(log)-1]
	if last.Action != state.GoalRework || last.Detail != "the review found a duplicated helper" {
		t.Fatalf("the note is recorded as work the goal owes: %+v", last)
	}
}

func TestGoalNoteAndReverse(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	if err := e.GoalNote(ctx, g.ID, "also make it work on Windows"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(wt.Root(), g.ArtifactPath()))
	notes, _ := spec.ViewSection(string(raw), "Notes")
	if !strings.Contains(notes, "also make it work on Windows") {
		t.Fatalf("notes = %q", notes)
	}
	if _, err := store.AppendGoalEvent(ctx, g.ID, state.GoalPayload{Action: state.GoalDecision, Detail: "json by default", Alternative: "table by default"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, g.ID, domain.StageVerify, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := e.ReverseGoalDecision(ctx, g.ID, "d-1", "tables read better"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetFeature(ctx, g.ID)
	if got.Stage != domain.StageImplement {
		t.Fatalf("reversing a decision sends the goal back, stage = %s", got.Stage)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	var sawReverse bool
	for _, en := range log {
		if en.Action == state.GoalReversed && en.Ref == "D-1" && strings.Contains(en.Detail, "table by default") {
			sawReverse = true
		}
	}
	if !sawReverse {
		t.Fatalf("log = %+v", log)
	}
	if err := e.ReverseGoalDecision(ctx, g.ID, "D-9", ""); err == nil {
		t.Fatal("reversing a decision that does not exist must fail")
	}
}

func TestGoalReportAtTheHandOver(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	cards := goalCards(t, store, g.ID)
	tick(t, e, g.ID)
	verifyCard(t, e, store, root, cards[0].ID, "cache.txt")
	tick(t, e, g.ID)
	// the docs card is dropped: DW-2 is served by nothing left
	view, _ := e.GoalView(ctx, g.ID)
	docsCard, _ := view.Card(cards[1].ID)
	if err := e.goalDrop(ctx, view.Goal, docsCard.Feature, "no docs needed", "lead"); err != nil {
		t.Fatal(err)
	}
	e.goalLog(ctx, g.ID, state.GoalPayload{Action: state.GoalDecision, Detail: "skip the docs page", Alternative: "write CACHE.md", By: "lead"})
	e.goalLog(ctx, g.ID, state.GoalPayload{Action: state.GoalDeclined, Card: cards[0].ID, Ref: "rename cache dir", Detail: "the name is fine", By: "lead"})
	r := tick(t, e, g.ID)
	if !r.Finished {
		t.Fatalf("actions %v", r.Actions)
	}
	e.Drop(g.ID)
	if _, err := store.Transition(ctx, g.ID, domain.StageVerify, "auto"); err != nil {
		t.Fatal(err)
	}
	e.recordGoalChecks(view.Goal, []goalCheckResult{{Name: "done-when DW-1", OK: true, Status: "pass"}})
	res, err := e.Advance(ctx, g.ID, "auto")
	if err != nil || res.Status != StatusNeedsMerge {
		t.Fatalf("verify gate: %v %v %q", res.Status, err, res.Reason)
	}
	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Ready || rep.Partial != "DW-2 lost every card serving it" {
		t.Fatalf("report = %+v", rep)
	}
	if met, total := rep.Met(); met != 1 || total != 2 {
		t.Fatalf("met %d of %d: %+v", met, total, rep.DoneWhen)
	}
	if rep.DoneWhen[1].Status != DoneWhenNotMet || !strings.Contains(rep.DoneWhen[1].Evidence, "dropped") {
		t.Fatalf("DW-2 = %+v", rep.DoneWhen[1])
	}
	if len(rep.Decisions) != 1 || rep.Decisions[0].Ref != "D-1" || len(rep.Declined) != 1 {
		t.Fatalf("decisions %+v declined %+v", rep.Decisions, rep.Declined)
	}
	if rep.Cards[0].Commit == "" || rep.Cards[1].State != "dropped" {
		t.Fatalf("cards = %+v", rep.Cards)
	}
	raw, _ := os.ReadFile(filepath.Join(root, g.ArtifactPath()))
	body, _ := spec.ViewSection(string(raw), spec.GoalSectionReport)
	for _, want := range []string{"1 of 2 done-when items met", "✓ DW-1", "✗ DW-2", "D-1 skip the docs page", "Declined findings"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report section lacks %q:\n%s", want, body)
		}
	}
}

func TestRaisingTheBudgetLiftsABudgetWrapUp(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	if err := e.goalWrapUp(ctx, g.ID, "the budget reached the reserve", ActorGoal); err != nil {
		t.Fatal(err)
	}
	if err := e.RaiseGoalBudget(ctx, g.ID, 6000); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetFeature(ctx, g.ID); got.Goal.WrappingUp() {
		t.Fatal("more budget lifts a wrap-up forced by the budget")
	}
	if err := e.StopGoal(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.RaiseGoalBudget(ctx, g.ID, 7000); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetFeature(ctx, g.ID); !got.Goal.WrappingUp() {
		t.Fatal("a stop you asked for stands through a raise")
	}
}

// A dropped card the goal replaced leaves nothing unmet: the goal finishes
// whole. Only an item that lost every card serving it makes it partial.
func TestDroppedButReplacedCardLeavesTheGoalWhole(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()
	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "", 1)
	doc = strings.Replace(doc, "- title: document the cache\n  serves: [DW-2]\n", "- title: document the cache\n  serves: [DW-2]\n- title: docs again\n  serves: [DW-2]\n", 1)
	g := goalAtPlan(t, store, wt, doc, 6000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	cards := goalCards(t, store, g.ID)
	view, _ := e.GoalView(ctx, g.ID)
	docs, _ := view.Card(cards[1].ID)
	if err := e.goalDrop(ctx, view.Goal, docs.Feature, "replaced", "lead"); err != nil {
		t.Fatal(err)
	}
	verifyCard(t, e, store, root, cards[0].ID, "cache.txt")
	tick(t, e, g.ID)
	verifyCard(t, e, store, root, cards[2].ID, "DOCS.md")
	var finished bool
	for i := 0; i < 4 && !finished; i++ {
		finished = tick(t, e, g.ID).Finished
	}
	if !finished {
		t.Fatal("goal should finish")
	}
	if got, _ := store.GetFeature(ctx, g.ID); got.Goal.Partial != "" {
		t.Fatalf("a replaced card leaves the goal whole, got partial %q", got.Goal.Partial)
	}
}

// TestTheGoalsReviewIsToldWhatTheGoalSettled: a goal that dropped the only
// card serving a done-when item has already decided that item is out of
// reach. Its review must be told, because the review's own contract makes
// an unmet item a blocking finding — and a blocking finding sends the goal
// back to a lead with no card left to send it to, which is the loop that
// burns the rework cap and hands the goal to a human. The scope note and
// the report's partial reason come from one derivation, so they can never
// name different items.
func TestTheGoalsReviewIsToldWhatTheGoalSettled(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "", 1)
	g := goalAtPlan(t, store, wt, doc, 6000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	view, err := e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := goalReviewScope(view); got != "" {
		t.Fatalf("a goal that gave nothing up tells its review nothing:\n%s", got)
	}

	cards := goalCards(t, store, g.ID)
	docs, ok := view.Card(cards[1].ID)
	if !ok {
		t.Fatalf("%s is not a card of %s", cards[1].ID, g.ID)
	}
	if err := e.goalDrop(ctx, view.Goal, docs.Feature, "the goal is wrapping up: no budget left", "lead"); err != nil {
		t.Fatal(err)
	}
	view, err = e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := goalReviewScope(view)
	for _, want := range []string{
		"DW-2",                       // the item that lost its card
		"the docs mention the cache", // in the reader's own words
		"Out of scope",
		"NOT blocking",
		string(docs.Feature.ID), // the card that was dropped
		"no budget left",        // and why
	} {
		if !strings.Contains(scope, want) {
			t.Fatalf("the review's scope note must carry %q:\n%s", want, scope)
		}
	}
	if got := goalPartialReason(view); !strings.Contains(got, "DW-2") {
		t.Fatalf("the report's partial reason comes from the same derivation, got %q", got)
	}
}

// TestStoppingAGoalBehindItsOwnReviewStillFinishesIt: a finished session
// of the goal's own — its review, left registered so a reader can read it
// — is what goalView reports as Reviewing, and Decide returns no actions
// at all while that is true. Stopping the goal then stamped a wrap-up
// nothing would carry out: no verified card landed, nothing was dropped,
// and the goal never came back partial, while the board said "wrapping
// up". The stop drops the finished session so its own wrap-up has a
// conductor to run it.
func TestStoppingAGoalBehindItsOwnReviewStillFinishesIt(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v", res.Status, err)
	}
	tick(t, e, g.ID)

	// the goal's own review, run to completion and left registered
	cur, err := store.GetFeature(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RunCritique(cur, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		s := e.Get(g.ID)
		if s != nil && !s.Live() {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the goal's review never finished")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// the conductor is switched off behind it: this is the state the stop
	// used to be offered in, and silently do nothing in
	if view, verr := e.GoalView(ctx, g.ID); verr != nil {
		t.Fatal(verr)
	} else if !view.Input.Reviewing {
		t.Fatal("a registered session is what the conductor reads as reviewing")
	} else if acts := goalpolicy.Decide(view.Input); len(acts) != 0 {
		t.Fatalf("nothing is conducted while it reviews: %v", acts)
	}

	if err := e.StopGoal(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if s := e.Get(g.ID); s != nil {
		t.Fatal("the stop drops the finished review so its wrap-up has a conductor")
	}
	if r := tick(t, e, g.ID); !r.Finished && len(r.Actions) == 0 {
		t.Fatal("a stopped goal must actually finish, not sit stamped as wrapping up")
	}
	got, _ := store.GetFeature(ctx, g.ID)
	if got.Goal.Partial == "" {
		t.Fatal("and comes back partial, as the stop promised")
	}
}

// TestAdoptingACardWhoseBranchCannotMoveChangesNothing: a card in a goal
// forks from the goal branch, so its recorded fork point is a commit main
// has never seen. The adoption used to reopen the card, clear its goal and
// only then try to move the branch — and report success when that move
// failed, handing back a card on the open board still anchored to the goal
// branch. Every later drift check reads that as main having been rewritten
// under it, and the board refuses to drive it. A move that cannot be made
// is now a refusal that leaves the card exactly as it was.
func TestAdoptingACardWhoseBranchCannotMoveChangesNothing(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()

	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	// the goal branch carries a file main has never seen
	goalDir := filepath.Join(root, g.WorktreePath())
	if err := os.WriteFile(filepath.Join(goalDir, "shared.txt"), []byte("from the goal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, goalDir, "add", "-A")
	gitIn(t, goalDir, "commit", "-q", "-m", "goal scaffolding")

	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	cards := goalCards(t, store, g.ID)
	card := cards[0]
	// its own commit edits the goal branch's file, so replaying it onto
	// main — which does not have the file at all — cannot apply
	gm, err := e.pool.ManagerFor(ctx, &card)
	if err != nil {
		t.Fatal(err)
	}
	cardDir, err := gm.Create(ctx, &card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cardDir, "shared.txt"), []byte("from the card\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, cardDir, "add", "-A")
	gitIn(t, cardDir, "commit", "-q", "-m", "card work")

	view, _ := e.GoalView(ctx, g.ID)
	gc, _ := view.Card(card.ID)
	if err := e.goalDrop(ctx, view.Goal, gc.Feature, "no budget left", "lead"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Adopt(ctx, card.ID, "user"); err == nil {
		t.Fatal("adopting a card whose branch cannot leave the goal branch must be refused")
	} else if !strings.Contains(err.Error(), string(g.ID)) {
		t.Fatalf("the refusal names the goal to land first: %v", err)
	}
	got, err := store.GetFeature(ctx, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.InGoal() || !got.GoalDropped() {
		t.Fatalf("a refused adoption leaves the card in its goal: %+v", got)
	}
	if got.Stage != domain.StageDone {
		t.Fatalf("and leaves it closed, adoptable again once the goal has landed: stage %s", got.Stage)
	}
}

// TestAGoalOutOfBudgetAsksInsteadOfDropping: which work to abandon when
// the money runs out is not the goal's decision — the envelope is a hard
// ceiling only a person raises. It used to drop whichever card had
// exhausted (reliably the most ambitious one, since that is the card that
// runs out first), wrap up, and then review itself against the work it
// had just defunded. Now it stops, says what the card needs, and keeps
// everything: the card holds its branch and its spend, and a raise of the
// goal's envelope answers the question.
func TestAGoalOutOfBudgetAsksInsteadOfDropping(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	doc := strings.Replace(testGoalDoc, "  depends_on: [local cache for export]\n", "", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	cards := goalCards(t, store, g.ID)
	// its first card ran out of envelope, and what the goal has left
	// cannot buy even one more turn: cards hold 600 + 1530, the reserve
	// holds 600, so 1250 of own spend leaves 20 against a 30-credit floor
	spender := cards[0]
	if _, err := store.Transition(ctx, spender.ID, domain.StagePlan, state.ActorAutopilot); err != nil {
		t.Fatal(err)
	}
	enterStage(t, store, spender.ID, domain.StagePlan, "gen-1")
	if err := store.AddSpend(ctx, spender.ID, float64(spender.Budget.Envelope), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.OpenDecision(ctx, spender.ID, domain.StagePlan, state.DecisionPayload{
		ID: "budget:" + string(spender.ID) + ":plan:1", Kind: state.DecisionKindBudget,
		Question: "plan hit its budget — top up or park.",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(ctx, g.ID, 1250, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	res := tick(t, e, g.ID)
	if !res.NeedsBudget.Waiting() {
		t.Fatalf("a goal that cannot fund its card asks for more: %v", res.Actions)
	}
	if res.NeedsBudget.Card != spender.ID || res.NeedsBudget.Needs <= spender.Budget.Envelope {
		t.Fatalf("it names the card and what it needs: %+v", res.NeedsBudget)
	}
	for _, a := range res.Actions {
		if a.Kind == goalpolicy.Drop || a.Kind == goalpolicy.WrapUp {
			t.Fatalf("running out of budget drops nothing and wraps nothing up: %v", a)
		}
	}
	got, _ := store.GetFeature(ctx, spender.ID)
	if got.GoalDropped() {
		t.Fatal("the waiting card keeps its place in the goal, its branch and its spend")
	}

	// the report says it is waiting, not that it is done
	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.NeedsBudget.Waiting() {
		t.Fatal("the hand-over carries the request")
	}
	var waiting int
	for _, d := range rep.DoneWhen {
		if d.Status == DoneWhenWaiting {
			waiting++
		}
	}
	if waiting == 0 {
		t.Fatalf("an item whose card is waiting is not one the goal gave up on: %+v", rep.DoneWhen)
	}
	if body := RenderGoalReport(rep); !strings.Contains(body, "Waiting for you") || !strings.Contains(body, "--envelope") {
		t.Fatalf("the rendered report says it is waiting and how to continue:\n%s", body)
	}

	// raising the goal's envelope answers it, and the goal conducts again
	if err := e.RaiseGoalBudget(ctx, g.ID, 8000); err != nil {
		t.Fatal(err)
	}
	view, err := e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.NeedsBudget.Waiting() {
		t.Fatal("more budget is the answer to the question")
	}
}

// The measured failure. A goal's own checks run in the goal tree — the
// baseline at plan approval, its verify later — and a repository whose
// suite regenerates a tracked file leaves one modified there. The goal
// tree is what every card lands on, so that file refused the next
// landing, the goal's run ended with it, and a resume hit the same wall:
// the only way out was for a person to find a directory under .gummi and
// clean it by hand. The conductor puts the tree back instead, and says
// that it did.
func TestACheckRunInTheGoalTreeDoesNotStopTheNextLanding(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	root := wt.Root()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("goal plan gate: %v %v", res.Status, err)
	}
	cache := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID)
	verifyCard(t, e, store, root, cache.ID, "cache.txt")

	// the repo's own test suite, run in the goal tree, rewrites a tracked
	// file — a golden, a generated doc, a snapshot of the machine it ran on
	goalDir := filepath.Join(root, g.WorktreePath())
	if err := os.WriteFile(filepath.Join(goalDir, "README.md"), []byte("regenerated\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if r := tick(t, e, g.ID); !r.Again {
		t.Fatalf("a landing asks for another tick: %v", r.Actions)
	}
	landed, _ := store.GetFeature(ctx, cache.ID)
	if landed.Stage != domain.StageDone || landed.LandedSHA == "" {
		t.Fatalf("the card did not land over a check run's leftovers: %+v", landed)
	}
	if out, err := os.ReadFile(filepath.Join(goalDir, "README.md")); err != nil || string(out) != "x\n" {
		t.Fatalf("the goal tree was not put back: %q %v", out, err)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	var said bool
	for _, en := range log {
		if en.Action == state.GoalTidied && strings.Contains(en.Detail, "README.md") {
			said = true
		}
	}
	if !said {
		t.Errorf("restoring the tree happened silently: %+v", log)
	}
}

// A note is delivered to the conductor, which reads it at implement. One
// that arrives while the goal is reviewing, verifying or already ready
// for you has nobody left to read it — so the hand-over says so, instead
// of leaving it in the doc for nobody.
func TestANoteThatArrivedTooLateIsOnTheHandOver(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("goal plan gate: %v %v", res.Status, err)
	}
	if _, err := store.AppendGoalEvent(ctx, g.ID, state.GoalPayload{Action: state.GoalLeadTurn, Detail: "kickoff"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := e.GoalNote(ctx, g.ID, "the hex path is broken too"); err != nil {
		t.Fatal(err)
	}
	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unread) != 1 || rep.Unread[0].Detail != "the hex path is broken too" {
		t.Fatalf("unread notes = %+v", rep.Unread)
	}
	if !strings.Contains(RenderGoalReport(rep), "Notes nobody read") {
		t.Errorf("the report does not say the note went unread:\n%s", RenderGoalReport(rep))
	}

	// a lead turn after it reads it; it is no longer unread
	if _, err := store.AppendGoalEvent(ctx, g.ID, state.GoalPayload{Action: state.GoalLeadTurn, Detail: "read the note"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rep, err = e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unread) != 0 {
		t.Errorf("a note the lead has read is still listed as unread: %+v", rep.Unread)
	}
}

// A goal that wrapped up because its lead kept failing must be
// recoverable: the failures that caused it are the newest lead entries in
// its log, and only a successful lead turn ever cleared them — which the
// conductor will not run while it is wrapping up. Lifting the wrap-up
// therefore lasted exactly one tick, which re-wrapped it in the same
// second and sent the same partial hand-over back.
func TestASendBackLiftsAWrapUpForGood(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	g := goalAtPlan(t, store, wt, testGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("goal plan gate: %v %v", res.Status, err)
	}
	for i := 0; i < goalpolicy.MaxLeadFailures; i++ {
		if _, err := store.AppendGoalEvent(ctx, g.ID, state.GoalPayload{
			Action: state.GoalLeadFailed, Detail: "claude run failed: exit status 1", By: ActorGoal,
		}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Input.LeadFailures < goalpolicy.MaxLeadFailures {
		t.Fatalf("failures = %d, want the wrap-up threshold", view.Input.LeadFailures)
	}

	// The composer's own route to the same place: a line read as "this
	// does not match the plan" moves the goal verify → implement without
	// writing a rework row, which is how the measured drive's second
	// send-back slipped past the reset and re-wrapped in the same second.
	if _, err := store.Transition(ctx, g.ID, domain.StageVerify, "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, g.ID, domain.StageImplement, "user"); err != nil {
		t.Fatal(err)
	}
	if v, verr := e.GoalView(ctx, g.ID); verr != nil {
		t.Fatal(verr)
	} else if v.Input.LeadFailures != 0 {
		t.Errorf("a goal put back to work carries %d old lead failures", v.Input.LeadFailures)
	}

	if err := e.SendBackGoal(ctx, g.ID, "the backend was out of quota; do the work", "user"); err != nil {
		t.Fatal(err)
	}
	view, err = e.GoalView(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Input.LeadFailures != 0 {
		t.Errorf("a send-back left %d failures standing — the goal re-wraps on its next tick", view.Input.LeadFailures)
	}
	if view.Input.WrapUp {
		t.Error("the goal is still wrapping up after a send-back")
	}
	for _, a := range goalpolicy.Decide(view.Input) {
		if a.Kind == goalpolicy.WrapUp {
			t.Fatalf("the tick after a send-back wrapped the goal up again: %v", a)
		}
	}
}
