package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/experiment"
)

// TestARunThatNeverTookTheSubstrateSaysNothingAboutTheRig: the run tally
// beside a pass is what a reader uses to decide how far to believe it —
// "a pass from a rig that wavered reads differently from one that never
// did" (§17.8). A run that was held, or that could not fit before an
// expiry, never touched the rig, so counting it as a run that judged
// nothing describes a rig that wavered when none did.
func TestARunThatNeverTookTheSubstrateSaysNothingAboutTheRig(t *testing.T) {
	x := GoalExperiment{Name: "fabric-matrix", Runs: []experiment.Result{
		{ID: "r1", State: experiment.StateDone, Outcome: experiment.Pass, Seconds: 60},
		{ID: "r2", State: experiment.StateDone, Outcome: experiment.Fail, Seconds: 60},
		{ID: "r3", State: experiment.StateDone, Outcome: experiment.Inconclusive, Seconds: 30, Flaky: true},
		{ID: "r4", State: experiment.StateDone, Outcome: experiment.NotRun},
		{ID: "r5", State: experiment.StateDone, Outcome: experiment.NotRun},
	}}
	got := reportExperiment(x)
	if got.Runs != 3 {
		t.Errorf("runs = %d, want 3 — two of the five never took the substrate", got.Runs)
	}
	if got.Conclusive != 2 {
		t.Errorf("conclusive = %d, want 2", got.Conclusive)
	}
	if got.Inconclusive != 1 {
		t.Errorf("judged nothing = %d, want 1 — the rig wavered once, not three times", got.Inconclusive)
	}
	if got.Flaky != 1 {
		t.Errorf("flaky = %d, want 1", got.Flaky)
	}
}

// TestARunIsNotOrderedWhileOneIsInFlight: one run at a time per goal
// (§17.9). The conductor's guard is the snapshot it ticked on, so a tick
// that began before the previous run's record was written sees no run in
// flight and orders another. The second run then exists only to be refused
// the lease it was made to take — a directory, a job, and a line in the
// tally that says the rig judged nothing.
func TestARunIsNotOrderedWhileOneIsInFlight(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	e, _, store, wt := advanceEngine(t)
	experimentRig(t, e, wt, "true")
	// a spawner that leaves the run in flight, as a detached runner does
	e.SetExperimentSpawner(func(string) error { return nil })
	g := goalAtPlan(t, store, wt, experimentGoalDoc, 4000)
	if _, err := e.Advance(context.Background(), g.ID, "user"); err != nil {
		t.Fatal(err)
	}
	goal, err := store.GetFeature(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.StartExperiment(context.Background(), goal, ExperimentStart{Name: "matrix", Purpose: PurposeIntegration})
	if err != nil {
		t.Fatalf("the first run: %v", err)
	}
	second, err := e.StartExperiment(context.Background(), goal, ExperimentStart{Name: "matrix", Purpose: PurposeIntegration})
	if err == nil {
		t.Fatalf("a second run was made while %s was in flight: %s", first.ID, second.ID)
	}
	if !strings.Contains(err.Error(), "already in flight") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if n := len(e.ExperimentRuns(g.ID)); n != 1 {
		t.Errorf("%d run directories, want 1 — the refused run left one behind", n)
	}
}
