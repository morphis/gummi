package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// TestStopCancelsRunningCommandStep: stopping a run kills the command a
// run: step is in the middle of, before the run's end checks and report
// read the tree; the command writes nothing after the stop.
func TestStopCancelsRunningCommandStep(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: gen, run: \"touch started.txt; sleep 120; touch late.txt\" }\n"})
	tickRuns(t, e, f.ID, run.N, "the command started", func(*domain.Run) bool {
		_, err := os.Stat(filepath.Join(tree, "started.txt"))
		return err == nil
	})
	began := time.Now()
	if err := e.WorkflowStop(ctx, f.ID, "enough"); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "ended", ended)
	if r.State != domain.RunStopped {
		t.Fatalf("ended %s: %s", r.State, r.Reason)
	}
	waitUntil(t, "the command's claim let go", func() bool {
		return !e.wfRT().isBusy(stepKey(f.ID, run.N, "gen"))
	})
	if d := time.Since(began); d > 20*time.Second {
		t.Fatalf("the stop waited %s on the command", d)
	}
	if s := stepPaths(t, e, f.ID, run.N)["gen"]; s.State != domain.StepStopped {
		t.Fatalf("step %+v", s)
	}
	if _, err := os.Stat(filepath.Join(tree, "late.txt")); err == nil {
		t.Fatal("the command ran on after the stop")
	}
}

// TestRetrySkipsEarlierAttempts: a retry re-runs the steps that failed and
// leaves the attempts it kept earlier alone: they are not renamed again,
// nor counted as steps to run again.
func TestRetrySkipsEarlierAttempts(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: bad\nsteps:\n  - { id: gen, run: \"exit 1\" }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	for range 2 {
		if err := e.WorkflowRetry(ctx, f.ID, run.N); err != nil {
			t.Fatal(err)
		}
		tickRuns(t, e, f.ID, run.N, "ended again", func(r *domain.Run) bool { return ended(r) })
	}
	paths := stepPaths(t, e, f.ID, run.N)
	for p := range paths {
		if strings.Count(p, "@") > 1 {
			t.Errorf("an earlier attempt was renamed as a failed step: %s", p)
		}
	}
	for _, want := range []string{"gen", "gen@1", "gen@2"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("no step %s among %v", want, paths)
		}
	}

	// a run whose only failed step is an earlier attempt has nothing to
	// run again
	steps := []domain.RunStep{
		{Path: "a", State: domain.StepDone},
		{Path: "a@1", State: domain.StepFailed},
	}
	r, _ := store.Run(ctx, f.ID, run.N)
	r.OnMax = nil
	if why := domain.RetryRefusal(*r, steps, nil); why == "" {
		t.Error("a retry was offered for an earlier attempt")
	}
}

// TestEndedRunReportRedeliveredAfterRestart: a run that ended and lost its
// report to a crash before the delivery has it written and sent on the next
// tick, once.
func TestEndedRunReportRedeliveredAfterRestart(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: quick\nsteps:\n  - { id: gen, run: \"echo hi\" }\n"})
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	reported := func() int {
		n := 0
		for _, m := range wf.sessionTurns() {
			if strings.Contains(m, "has reported") {
				n++
			}
		}
		return n
	}
	waitUntil(t, "the report delivered", func() bool {
		r, _ := store.Run(ctx, f.ID, run.N)
		return r != nil && !r.ReportPending && reported() == 1
	})
	// the crash: the end was written, the delivery was not
	r, _ := store.Run(ctx, f.ID, run.N)
	r.ReportPending = true
	if err := store.UpdateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	reportFile := filepath.Join(e.RunDir(f.ID, run.N), workflowrecipe.RunReportFile)
	if err := os.Remove(reportFile); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the report delivered again", func() bool {
		if _, err := e.WorkflowTick(ctx); err != nil {
			t.Fatal(err)
		}
		r, _ := store.Run(ctx, f.ID, run.N)
		return r != nil && !r.ReportPending && reported() == 2
	})
	if _, err := os.Stat(reportFile); err != nil {
		t.Fatalf("the run report not written again: %v", err)
	}
	for range 3 {
		_, _ = e.WorkflowTick(ctx)
	}
	if n := reported(); n != 2 {
		t.Fatalf("the report was sent %d times", n)
	}
}

// TestParallelBranchesShareRemainingPool: the steps of a parallel block each
// get a share of what the session's pool has left, so their caps together
// are not past it.
func TestParallelBranchesShareRemainingPool(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		wf.on(id, func(string, string, int) wfReply {
			<-g.ch
			return wfReply{text: "done"}
		})
	}
	e, store, f, _ := wfEngine(t, wf)
	e.cfg.TurnReserve = 1
	ctx := context.Background()
	if err := e.RaiseEnvelope(ctx, f.ID, 100); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: four\nsteps:\n  - id: p\n    parallel:\n      join: all\n      steps:\n" +
		"        - { id: a, role: scout, prompt: look }\n        - { id: b, role: scout, prompt: look }\n" +
		"        - { id: c, role: scout, prompt: look }\n        - { id: d, role: scout, prompt: look }\n"})
	tickRuns(t, e, f.ID, run.N, "all four at work", func(*domain.Run) bool {
		n := 0
		for _, id := range []string{"a", "b", "c", "d"} {
			n += wf.count(id)
		}
		return n == 4
	})
	sess, _ := store.GetFeature(ctx, f.ID)
	left, bounded := e.sessionPoolLeft(ctx, sess)
	if !bounded || left <= 0 {
		t.Fatalf("pool left %v %v", left, bounded)
	}
	total := 0.0
	for _, s := range stepPaths(t, e, f.ID, run.N) {
		card, err := store.GetFeature(ctx, s.Card)
		if err != nil {
			t.Fatal(err)
		}
		b := e.stepBudget(ctx, card)
		if b <= 0 {
			t.Errorf("step %s has no cap", s.Path)
		}
		total += b
	}
	if total > left+0.001 {
		t.Fatalf("the four caps total %.1f, past the pool's remainder %.1f", total, left)
	}
}

// TestStoppedStepSpendRefreshed: a step that settles stopped, and a run that
// ends, carry what the step's card has spent by then, not what the last
// tick saw.
func TestStoppedStepSpendRefreshed(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	run := &domain.Run{
		Session: f.ID, RecipeName: "x", State: domain.RunRunning, Spec: 1, SpecRev: 1, StartTree: "t",
		Recipe: domain.Recipe{Name: "x", Steps: []domain.Entry{{Step: &domain.Step{ID: "a", Role: domain.RoleScout, Prompt: "look"}}}},
	}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	cardOf := func(num int) domain.FeatureID {
		c := freeformCard(num, "step card")
		createFeature(t, store, c)
		if err := store.AddSpend(ctx, c.ID, 7, 0, 10, 10); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	live, gone := cardOf(2), cardOf(3)
	for _, s := range []domain.RunStep{
		{Session: f.ID, Run: run.N, Path: "a", StepID: "a", Role: domain.RoleScout, State: domain.StepRunning, Card: live},
		{Session: f.ID, Run: run.N, Path: "b", StepID: "a", Role: domain.RoleScout, State: domain.StepStopped, Card: gone},
	} {
		if err := store.CreateRunStep(ctx, &s); err != nil {
			t.Fatal(err)
		}
	}
	want := 0.0
	if c, err := store.GetFeature(ctx, live); err == nil {
		want = c.Spend.CreditEquivalent()
	}
	if want <= 0 {
		t.Fatal("the card has no spend")
	}
	e.settleStep(ctx, f.ID, run.N, "a", domain.StepStopped, "stopped")
	if s := stepPaths(t, e, f.ID, run.N)["a"]; s.Spent != want {
		t.Fatalf("a settled step's spend is %v, the card's is %v", s.Spent, want)
	}
	e.endRun(ctx, f.ID, run.N, domain.RunStopped, "enough")
	r, _ := store.Run(ctx, f.ID, run.N)
	if r.State != domain.RunStopped || r.Spent != 2*want {
		t.Fatalf("run %s spent %v, want %v", r.State, r.Spent, 2*want)
	}
	if s := stepPaths(t, e, f.ID, run.N)["b"]; s.Spent != want {
		t.Fatalf("an earlier-stopped step's spend is %v, the card's is %v", s.Spent, want)
	}
}

// TestPauseLetsACommandStepFinish: a pause does not kill a running command
// step — it spends nothing, and one that is not safe to run twice must not
// run again — so the command runs once, to its end, while the run is parked.
func TestPauseLetsACommandStepFinish(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: slow\nsteps:\n  - { id: gen, run: \"echo x >> n.txt; while [ ! -e go ]; do sleep 0.05; done\" }\n"})
	tickRuns(t, e, f.ID, run.N, "the command started", func(*domain.Run) bool {
		_, err := os.Stat(filepath.Join(tree, "n.txt"))
		return err == nil
	})
	if err := e.WorkflowPause(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tickRuns(t, e, f.ID, run.N, "the command finished while paused", func(r *domain.Run) bool {
		return r.State == domain.RunPaused && stepPaths(t, e, f.ID, run.N)["gen"].State == domain.StepDone
	})
	if err := e.WorkflowResume(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	if b, _ := os.ReadFile(filepath.Join(tree, "n.txt")); string(b) != "x\n" {
		t.Fatalf("the command ran %q, want once", b)
	}
}

// TestWatchAndCommandStepsDoNotShrinkAStepsCap: a live watch and a card-less
// command step spend nothing from the pool, so they are not siblings that a
// working step shares it with.
func TestWatchAndCommandStepsDoNotShrinkAStepsCap(t *testing.T) {
	wf := newWFFake()
	g := newGate(t)
	wf.on("a", func(string, string, int) wfReply {
		<-g.ch
		return wfReply{text: "done"}
	})
	e, store, f, _ := wfEngine(t, wf)
	e.cfg.TurnReserve = 1
	ctx := context.Background()
	if err := e.RaiseEnvelope(ctx, f.ID, 100); err != nil {
		t.Fatal(err)
	}
	run := proposeAndAllow(t, e, f.ID, WorkflowRequest{RecipeYAML: "name: one\nsteps:\n  - { id: a, role: scout, prompt: look }\n"})
	tickRuns(t, e, f.ID, run.N, "a at work", func(*domain.Run) bool { return wf.count("a") == 1 })
	sess, _ := store.GetFeature(ctx, f.ID)
	left, _ := e.sessionPoolLeft(ctx, sess)
	var card domain.Feature
	for _, s := range stepPaths(t, e, f.ID, run.N) {
		card, _ = store.GetFeature(ctx, s.Card)
	}
	for i, st := range []domain.StepState{domain.StepWatching, domain.StepRunning} {
		if err := store.CreateRunStep(ctx, &domain.RunStep{Session: f.ID, Run: run.N, Path: fmt.Sprintf("x%d", i), StepID: "x", State: st}); err != nil {
			t.Fatal(err)
		}
	}
	if b := e.stepBudget(ctx, card); b < left-0.001 {
		t.Fatalf("cap %.1f of %.1f: a watch or a command step took a share", b, left)
	}
}
