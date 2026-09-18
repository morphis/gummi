package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

const experimentGoalDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: a deployed build serves the cache\n  experiment: matrix\n" +
	"- id: DW-2\n  says: the first probe of the matrix holds\n  experiment: matrix\n  assertions: [first]\n```\n\n" +
	"## Limits\n\nNone.\n\n" +
	"## Budget\n\nAbout 1500 credits.\n\n```gummi-goal\nlanes: 1\nruns: 10\nminutes: 600\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: local cache for export\n  serves: [DW-1, DW-2]\n  envelope: 600\n```\n\n" +
	"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n## Verification plan\n\nRun the matrix.\n\n## Report\n\n\n"

// experimentRig configures a substrate made of files and an experiment
// that passes when the deployed tree carries cache.txt. run is the
// experiment's run command.
func experimentRig(t *testing.T, e *Engine, wt *worktree.Manager, run string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rig := t.TempDir()
	cfg := "substrates:\n  rig:\n    probe: test -f " + rig + "/up\n    provision: touch " + rig + "/up\n    reset: echo r >> " + rig + "/resets\n" +
		"experiments:\n  matrix:\n    substrate: rig\n    run: |\n      " + run + "\n"
	if err := os.WriteFile(filepath.Join(wt.Root(), ".gummi", "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// in-process and synchronous: the run is over by the time the tick that
	// asked for it returns
	e.SetExperimentSpawner(func(dir string) error {
		job, err := experiment.LoadJob(dir)
		if err != nil {
			return err
		}
		experiment.Execute(context.Background(), job)
		return nil
	})
	return rig
}

const matrixRun = `printf '{"id":"first","ok":true}\n' > "$GUMMI_EVIDENCE/results.ndjson"; if test -f "$GUMMI_TREE_HOME/cache.txt"; then printf '{"id":"second","ok":true}\n' >> "$GUMMI_EVIDENCE/results.ndjson"; else printf '{"id":"second","ok":false,"detail":"no cache"}\n' >> "$GUMMI_EVIDENCE/results.ndjson"; exit 1; fi`

func TestThePlanGateRefusesAnExperimentNobodyConfigured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	e, _, store, wt := advanceEngine(t)
	g := goalAtPlan(t, store, wt, experimentGoalDoc, 4000)
	res, err := e.Advance(context.Background(), g.ID, "user")
	if err != nil || res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, `experiment "matrix"`) {
		t.Fatalf("%v %v %q", res.Status, err, res.Reason)
	}
}

// Settled work is proven on the heads the goal has before it is judged,
// and the evidence is what its verify and its hand-over read.
func TestAGoalProvesItsExperimentItemsBeforeItFinishes(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, matrixRun)
	g := goalAtPlan(t, store, wt, experimentGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	card := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID) // starts the card
	verifyCard(t, e, store, wt.Root(), card.ID, "cache.txt")
	if res := tick(t, e, g.ID); len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Land {
		t.Fatalf("lands: %v", res.Actions)
	}

	res := tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.Finished {
		t.Fatalf("settled, and unproven: it makes the run before it finishes: %v", res.Actions)
	}
	runs := e.ExperimentRuns(g.ID)
	if len(runs) != 1 || runs[0].Outcome != experiment.Pass || runs[0].Purpose != PurposeVerify {
		t.Fatalf("runs: %+v", runs)
	}
	view, _ := e.GoalView(ctx, g.ID)
	if len(view.Experiments) != 1 || view.Experiments[0].Evidence == nil || len(view.Experiments[0].Items) != 2 {
		t.Fatalf("the run is about the goal's heads: %+v", view.Experiments)
	}

	rep, err := e.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.DoneWhen {
		if d.Status != DoneWhenMet || !strings.Contains(d.Evidence, runs[0].ID) {
			t.Fatalf("both items are met on the run's evidence: %+v", d)
		}
	}
	if !strings.Contains(rep.DoneWhen[1].How, "[first]") {
		t.Fatalf("an item about some assertions says which: %q", rep.DoneWhen[1].How)
	}
	if body := RenderGoalReport(rep); !strings.Contains(body, "### Experiments") || !strings.Contains(body, "1 conclusive, 0 judged nothing") {
		t.Fatalf("the hand-over says how far the rig can be believed:\n%s", body)
	}

	// It passes. Before that is handed over as proof, the same experiment
	// is seen to be able to fail: once, on the trunk, which has no cache.
	res = tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.Actions[0].Reason != PurposeNegativeControl || res.Finished {
		t.Fatalf("%v", res.Actions)
	}
	view, _ = e.GoalView(ctx, g.ID)
	if tr := view.Experiments[0].Trunk; tr == nil || tr.Outcome != experiment.Fail || !tr.ExpectFail {
		t.Fatalf("the trunk fails it, as it should: %+v", tr)
	}
	if view.Experiments[0].Evidence == nil || view.Experiments[0].Evidence.ID != runs[0].ID {
		t.Fatal("a run about the trunk is not evidence about the goal")
	}
	rep, _ = e.GoalReport(ctx, g.ID)
	if !strings.Contains(rep.DoneWhen[0].Evidence, "NOT holding on the trunk") || !strings.Contains(rep.DoneWhen[1].Evidence, "holding on the trunk too") {
		t.Fatalf("a pass is worth what the same run says about the trunk — DW-1 is the goal's doing, DW-2's assertion held anyway:\n%s\n%s",
			rep.DoneWhen[0].Evidence, rep.DoneWhen[1].Evidence)
	}
	if res = tick(t, e, g.ID); !res.Finished {
		t.Fatalf("proven, it finishes: %v", res.Actions)
	}
	if got := e.ExperimentRuns(g.ID); len(got) != 2 {
		t.Fatalf("and does not pay for the same evidence twice: %d runs", len(got))
	}
}

// Evidence is about the heads it ran on. A landing moves them, and what
// was proven before it says nothing about what is there after.
func TestEvidenceGoesStaleWhenAHeadMoves(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, matrixRun)
	doc := strings.Replace(experimentGoalDoc, "  envelope: 600\n", "  envelope: 600\n- title: second card\n  serves: [DW-1]\n", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	goal, _ := store.GetFeature(ctx, g.ID)
	if _, err := e.StartExperiment(ctx, goal, ExperimentStart{Name: "matrix", Purpose: PurposeIntegration}); err != nil {
		t.Fatal(err)
	}
	view, _ := e.GoalView(ctx, g.ID)
	if ev := view.Experiments[0].Evidence; ev == nil || ev.Outcome != experiment.Fail {
		t.Fatalf("nothing has landed, so the matrix fails — and says so twice before it is believed: %+v", view.Experiments[0])
	}
	if held, ok := view.Experiments[0].Evidence.Holds([]string{"first"}); !held || !ok {
		t.Fatal("the assertion that held is evidence for the item that is only about it")
	}

	cards := goalCards(t, store, g.ID)
	tick(t, e, g.ID)
	verifyCard(t, e, store, wt.Root(), cards[0].ID, "cache.txt")
	tick(t, e, g.ID) // lands it: the home head moves
	view, _ = e.GoalView(ctx, g.ID)
	if view.Experiments[0].Evidence != nil {
		t.Fatalf("the old run is about heads the goal no longer has: %+v", view.Experiments[0].Evidence)
	}
	if len(view.Experiments[0].Runs) != 1 {
		t.Fatal("it is still on record")
	}
}

// Runs that keep judging nothing stop the goal; a person's return is what
// buys another.
func TestAGoalStopsMakingRunsThatJudgeNothing(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, "echo 'routing never converged'; exit 75")
	g := goalAtPlan(t, store, wt, experimentGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	card := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID)
	verifyCard(t, e, store, wt.Root(), card.ID, "cache.txt")
	tick(t, e, g.ID)

	var res GoalTickResult
	for i := 0; i < goalpolicy.MaxInconclusive+3; i++ {
		res = tick(t, e, g.ID)
		if res.Finished {
			t.Fatal("a goal with no evidence does not go on to be judged")
		}
	}
	if res.StalledExperiment != "matrix" || !strings.Contains(res.Stalled, "routing never converged") {
		t.Fatalf("it stops and says why: %+v", res)
	}
	if got := len(e.ExperimentRuns(g.ID)); got != goalpolicy.MaxInconclusive {
		t.Fatalf("%d runs; each is substrate time spent learning the substrate is not to be trusted", got)
	}
	if got, _ := store.GetFeature(ctx, card.ID); got.GoalDropped() || got.Stage != domain.StageDone {
		t.Fatal("nothing is dropped")
	}
	if err := e.GoalResumed(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if res = tick(t, e, g.ID); len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run {
		t.Fatalf("someone has been there: one more run: %v", res.Actions)
	}
	log, _ := store.GoalLog(ctx, g.ID)
	n := 0
	for _, en := range log {
		if en.Action == state.GoalRun {
			n++
		}
	}
	if n != goalpolicy.MaxInconclusive+1 {
		t.Fatalf("every run is in the goal's log: %d", n)
	}
}

// The substrate budget is a ceiling, its spend is read off the runs, and a
// goal that cannot afford its proof asks rather than going without.
func TestAGoalOutOfSubstrateBudgetAsksForMore(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, matrixRun)

	// the plan gate wants the second budget agreed wherever there is
	// something to spend it on
	none := strings.Replace(experimentGoalDoc, "runs: 10\nminutes: 600\n", "", 1)
	g := goalAtPlan(t, store, wt, none, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusBlockedGoalPlan || !strings.Contains(res.Reason, "substrate budget") {
		t.Fatalf("%v %v %q", res.Status, err, res.Reason)
	}
	writeArtifact(t, wt.Root(), g, strings.Replace(experimentGoalDoc, "runs: 10\nminutes: 600\n", "runs: 2\n", 1))
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	goal, _ := store.GetFeature(ctx, g.ID)
	for i := 0; i < 2; i++ { // two runs made early, by whoever
		if _, err := e.StartExperiment(ctx, goal, ExperimentStart{Name: "matrix", Purpose: PurposeIntegration}); err != nil {
			t.Fatal(err)
		}
	}
	view, _ := e.GoalView(ctx, g.ID)
	if view.Substrate.Runs != 2 || view.Substrate.RunsSpent != 2 {
		t.Fatalf("agreed at the gate, spent by the runs on disk: %+v", view.Substrate)
	}

	card := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID)
	verifyCard(t, e, store, wt.Root(), card.ID, "cache.txt")
	tick(t, e, g.ID)
	res := tick(t, e, g.ID)
	if !res.NeedsSubstrate.Waiting() || res.NeedsSubstrate.Experiment != "matrix" || res.Finished {
		t.Fatalf("settled, unproven and out of runs: it asks: %+v", res)
	}
	tick(t, e, g.ID)
	log, _ := store.GoalLog(ctx, g.ID)
	asked := 0
	for _, en := range log {
		if en.Action == state.GoalNeedSubstrate {
			asked++
		}
	}
	if asked != 1 || len(e.ExperimentRuns(g.ID)) != 2 {
		t.Fatalf("asked once (%d), and no run was made on credit", asked)
	}
	rep, _ := e.GoalReport(ctx, g.ID)
	if body := RenderGoalReport(rep); !rep.NeedsSubstrate.Waiting() || !strings.Contains(body, "--runs") || !strings.Contains(body, "2 of 2 runs") {
		t.Fatalf("the hand-over says it is waiting and how to continue:\n%s", body)
	}

	if err := e.RaiseGoalSubstrate(ctx, g.ID, 1, 0); err == nil {
		t.Fatal("a raise does not lower the ceiling")
	}
	if err := e.RaiseGoalSubstrate(ctx, g.ID, 5, 0); err != nil {
		t.Fatal(err)
	}
	if res = tick(t, e, g.ID); len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.NeedsSubstrate.Waiting() {
		t.Fatalf("raised, it makes the run: %+v", res)
	}
}

const threeCardGoalDoc = "# GL-001: Export works offline\n\n" +
	"## Objective\n\nExport works with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: a deployed build serves the cache\n  experiment: matrix\n```\n\n" +
	"## Limits\n\nNone.\n\n" +
	"## Budget\n\nAbout 1500 credits.\n\n```gummi-goal\nlanes: 1\nruns: 20\nintegrate_every: 2\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: one\n  serves: [DW-1]\n  envelope: 300\n" +
	"- title: two\n  serves: [DW-1]\n  envelope: 300\n" +
	"- title: three\n  serves: [DW-1]\n  envelope: 300\n```\n\n" +
	"## Notes\n\n\n## Try it\n\n\n## Review\n\n\n## Verification plan\n\nRun the matrix.\n\n## Report\n\n\n"

// "cache" holds once cache.txt is deployed and as long as break.txt is not.
const regressionRun = `if test -f "$GUMMI_TREE_HOME/cache.txt" && ! test -f "$GUMMI_TREE_HOME/break.txt"; then printf '{"id":"cache","ok":true}\n' > "$GUMMI_EVIDENCE/results.ndjson"; else printf '{"id":"cache","ok":false,"detail":"not served"}\n' > "$GUMMI_EVIDENCE/results.ndjson"; exit 1; fi`

// landNext starts, verifies and lands the goal's next waiting card.
func landNext(t *testing.T, e *Engine, store *state.Store, root string, goal domain.FeatureID, file string) domain.FeatureID {
	t.Helper()
	var id domain.FeatureID
	if res := tick(t, e, goal); len(res.Start) == 1 {
		id = res.Start[0].ID
	} else {
		// the tick that landed the previous card started this one already
		view, _ := e.GoalView(context.Background(), goal)
		for _, c := range view.Cards {
			if c.State == goalpolicy.Running {
				id = c.Feature.ID
				break
			}
		}
	}
	if id == "" {
		t.Fatal("no card is started or starting")
	}
	verifyCard(t, e, store, root, id, file)
	for i := 0; i < 3; i++ {
		for _, a := range tick(t, e, goal).Actions {
			if a.Kind == goalpolicy.Land && a.Card == id {
				return id
			}
		}
	}
	t.Fatalf("%s did not land", id)
	return id
}

// Something that held and no longer does is a landing's doing. The goal's
// landings are one commit each, so it bisects them, and names the card.
func TestARegressionIsBisectedToTheCardThatLandedIt(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, regressionRun)
	g := goalAtPlan(t, store, wt, threeCardGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	landNext(t, e, store, wt.Root(), g.ID, "cache.txt")
	goal, _ := store.GetFeature(ctx, g.ID)
	if r, err := e.StartExperiment(ctx, goal, ExperimentStart{Name: "matrix", Purpose: PurposeIntegration}); err != nil || r.ID == "" {
		t.Fatal(err)
	}
	view, _ := e.GoalView(ctx, g.ID)
	if x := view.Experiments[0]; x.Evidence == nil || x.Evidence.Outcome != experiment.Pass || len(x.Green) != 1 {
		t.Fatalf("the frontier: cache holds: %+v", x)
	}

	landNext(t, e, store, wt.Root(), g.ID, "other.txt")
	view, _ = e.GoalView(ctx, g.ID)
	if n := len(view.Experiments[0].LandedSince); n != 1 {
		t.Fatalf("one landing since the last run: %d", n)
	}
	culpritID := landNext(t, e, store, wt.Root(), g.ID, "break.txt")

	// settled and unproven: the goal makes its run, which fails — twice,
	// on a reset substrate, before it is believed
	res := tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.Actions[0].Reason != PurposeVerify {
		t.Fatalf("%v", res.Actions)
	}
	view, _ = e.GoalView(ctx, g.ID)
	x := view.Experiments[0]
	if len(x.Regressed) != 1 || x.Regressed[0] != "cache" || len(x.Suspects) != 2 || x.Culprit != "" || x.BisectNext != 0 {
		t.Fatalf("cache held and no longer does; two landings could have done it: %+v", x)
	}

	res = tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.Actions[0].Reason != PurposeBisect || res.Finished {
		t.Fatalf("it narrows it before it goes on: %v", res.Actions)
	}
	view, _ = e.GoalView(ctx, g.ID)
	x = view.Experiments[0]
	if x.Culprit != culpritID {
		t.Fatalf("held after the second card landed, not after the third: blamed %q, want %q", x.Culprit, culpritID)
	}
	if why := x.regressionWhy(); !strings.Contains(why, string(culpritID)) || !strings.Contains(why, "cache") {
		t.Fatalf("the lead is told which card and what: %s", why)
	}
	runs := e.ExperimentRuns(g.ID)
	if len(runs) != 3 || runs[2].Purpose != PurposeBisect {
		t.Fatalf("one bisect run was all it took: %d runs", len(runs))
	}
	// the snapshots a run deployed from are gone once it is over; the
	// evidence is not
	for _, r := range runs {
		if _, err := os.Stat(filepath.Join(r.Dir, "trees")); err == nil {
			t.Fatalf("%s still has its checkouts", r.ID)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "evidence", "results.ndjson")); err != nil {
			t.Fatalf("%s lost its evidence", r.ID)
		}
	}
}

// While work is in flight, what has landed is proven as soon as enough of
// it has piled up and the substrate is idle.
func TestLandingsAreProvenWhileOtherWorkIsStillInFlight(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, regressionRun)
	g := goalAtPlan(t, store, wt, threeCardGoalDoc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	landNext(t, e, store, wt.Root(), g.ID, "cache.txt")
	if got := len(e.ExperimentRuns(g.ID)); got != 0 {
		t.Fatalf("integrate_every is 2: one landing is not enough, got %d runs", got)
	}
	landNext(t, e, store, wt.Root(), g.ID, "other.txt")
	var made bool
	for i := 0; i < 3 && !made; i++ {
		for _, a := range tick(t, e, g.ID).Actions {
			made = made || (a.Kind == goalpolicy.Run && a.Reason == PurposeIntegration)
		}
	}
	runs := e.ExperimentRuns(g.ID)
	if !made || len(runs) != 1 || runs[0].Outcome != experiment.Pass {
		t.Fatalf("two landings and an idle substrate: an integration run, with the third card still to come: %+v", runs)
	}
}

// A live card proves itself on the substrate before it lands, on the goal's
// heads with its own branch in place of the goal's.
func TestALiveCardIsProvenOnItsOwnBranchBeforeItLands(t *testing.T) {
	e, _, store, wt := advanceEngine(t)
	ctx := context.Background()
	experimentRig(t, e, wt, regressionRun)
	doc := strings.Replace(experimentGoalDoc, "  envelope: 600\n", "  envelope: 600\n  live: true\n", 1)
	g := goalAtPlan(t, store, wt, doc, 4000)
	if res, err := e.Advance(ctx, g.ID, "user"); err != nil || res.Status != StatusAdvanced {
		t.Fatalf("advance: %v %v %q", res.Status, err, res.Reason)
	}
	card := goalCards(t, store, g.ID)[0]
	tick(t, e, g.ID)
	verifyCard(t, e, store, wt.Root(), card.ID, "cache.txt")

	res := tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Run || res.Actions[0].Card != card.ID {
		t.Fatalf("verified is not proven: a run first, not a landing: %v", res.Actions)
	}
	runs := e.ExperimentRuns(g.ID)
	if len(runs) != 1 || runs[0].Purpose != "card "+string(card.ID) || runs[0].Outcome != experiment.Pass {
		t.Fatalf("the run deployed the card's branch, which carries cache.txt: %+v", runs)
	}
	view, _ := e.GoalView(ctx, g.ID)
	if gc, _ := view.Card(card.ID); gc.LiveProof != goalpolicy.LivePassed || runs[0].Heads[""] != gc.LiveHead {
		t.Fatalf("the proof is about the head the card has: %+v", gc)
	}
	res = tick(t, e, g.ID)
	if len(res.Actions) != 1 || res.Actions[0].Kind != goalpolicy.Land {
		t.Fatalf("proven, it lands: %v", res.Actions)
	}
}
