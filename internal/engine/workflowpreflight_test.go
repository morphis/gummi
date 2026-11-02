package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// TestAPreflightGatesItsProposal: a recipe's preflight runs in the
// session's tree when the run is proposed. A non-zero exit refuses the
// proposal with what it printed and creates no run; a zero exit shows on
// the proposal and fills {{preflight.output}} in a step's brief.
func TestAPreflightGatesItsProposal(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	recipe := func(cmd string) string {
		return "name: pre\npreflight: " + cmd + "\nsteps:\n" +
			"  - {id: write, role: implementer, prompt: \"Start from {{preflight.output}}\\n\\n{{spec}}\"}\n"
	}
	_, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe(`"echo xdelta3 is missing; exit 3"`), Spec: 1})
	if err == nil || !strings.Contains(err.Error(), "exited 3") || !strings.Contains(err.Error(), "xdelta3 is missing") {
		t.Fatalf("a failing preflight: %v", err)
	}
	if runs, _ := store.SessionRuns(ctx, f.ID); len(runs) != 0 {
		t.Fatalf("a refused preflight left runs %+v", runs)
	}

	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipeYAML: recipe(`"echo 2 tests fail before the run"`), Spec: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.WorkflowProposal(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if p.Preflight != "echo 2 tests fail before the run" || p.PreflightOutput != "2 tests fail before the run" {
		t.Errorf("the proposal's preflight: %q → %q", p.Preflight, p.PreflightOutput)
	}
	if r, _ := store.Run(ctx, f.ID, run.N); r == nil || r.Pending == nil || !strings.Contains(r.Pending.Question, "Its preflight passed") {
		t.Errorf("the proposal's question: %+v", r)
	}
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	tickRuns(t, e, f.ID, run.N, "ended", ended)
	wf.mu.Lock()
	briefs := append([]string(nil), wf.stepT["write"]...)
	wf.mu.Unlock()
	if len(briefs) != 1 || !strings.Contains(briefs[0], "Start from <<<gummi-output step=\"preflight\">>>\n2 tests fail before the run") {
		t.Errorf("the step's brief:\n%s", briefs)
	}
}

// missingToolSpec's one check runs a command the tree does not have.
var missingToolSpec = strings.Replace(wfTestSpec, "check: test -f out.txt", "check: gummi-no-such-tool --verify", 1)

// TestACheckThatCannotRunIsShownAndAsks: a done-when check whose command
// is not found reads "cannot run" on the proposal, and an until: checks
// loop on it asks the person after one round instead of spinning to its
// max on a failure no change could fix.
func TestACheckThatCannotRunIsShownAndAsks(t *testing.T) {
	wf := newWFFake()
	e, _, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	if _, err := e.WriteSessionSpec(ctx, f, 1, missingToolSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{Recipe: "fix-until-green"})
	if err != nil {
		t.Fatal(err)
	}
	baselineDone(t, e, f.ID, run.N)
	p, err := e.WorkflowProposal(ctx, f.ID, run.N)
	if err != nil {
		t.Fatal(err)
	}
	if p.Baseline == nil || len(p.Baseline.Items) != 1 || p.Baseline.Items[0].Line != "DW-1 cannot run in this tree" || !p.Baseline.Items[0].Problem {
		t.Fatalf("the proposal's baseline: %+v", p.Baseline)
	}
	waitUntil(t, "the session heard of DW-1", func() bool { return sessionTold(wf, "DW-1", "cannot run in this tree (exit 127") })
	if err := e.WorkflowAnswer(ctx, f.ID, run.N, pendingID(t, e, f.ID, run.N), WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	r := tickRuns(t, e, f.ID, run.N, "asked", pendingOf(domain.RunDecideUnjudged))
	if !strings.Contains(r.Pending.Question, "the check of DW-1 cannot run in this tree") || wf.count("implement") != 1 {
		t.Fatalf("after %d iterations, decision %+v", wf.count("implement"), r.Pending)
	}
}
