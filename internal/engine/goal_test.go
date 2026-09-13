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
	// (4000 − 600 reserve) × 90% headroom − 600 for the first card = 2460
	if docs.Budget.Envelope != 2460 {
		t.Fatalf("docs card envelope = %d, want the rest of the pool", docs.Budget.Envelope)
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
	// 340 is left to give: 4000 − 600 − 2460 − 600 reserve
	if err := e.goalRaise(ctx, view.Goal, cache, 1000, "test", "lead"); err == nil || !strings.Contains(err.Error(), "left to give") {
		t.Fatalf("a raise past the goal budget must be refused, got %v", err)
	}
	if err := e.RaiseGoalBudget(ctx, g.ID, 4500); err != nil {
		t.Fatal(err)
	}
	view, _ = e.GoalView(ctx, g.ID)
	if err := e.goalRaise(ctx, view.Goal, view.Cards[0], 1000, "test", "lead"); err != nil {
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
