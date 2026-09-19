package engine

import (
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
