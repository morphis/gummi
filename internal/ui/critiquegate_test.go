package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// restartedAt rebuilds a board the way a fresh `gummi` start does: a
// card whose stage session was persisted by the process before it, the
// engine restored from those rows, and the needs-attention queue
// reconstructed from what the restore found. It is the scaffold for the
// stop this file is about — one that only ever exists ACROSS a restart,
// because live the loop consumes it before anyone sees it.
func restartedAt(t *testing.T, stage domain.Stage, snap state.SessionSnapshot) (*Shell, domain.Feature) {
	t.Helper()
	ws, store, wt := uiRepo(t)
	ctx := context.Background()

	f := mkFeature(t, store, 1, "rename the bridge", stage)
	snap.Feature, snap.Stage = f.ID, stage
	if err := store.SaveSession(ctx, snap); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("ok")), Store: store, Pool: wt,
		Workspace: ws, Model: "m", Persist: true,
	})
	t.Cleanup(func() { eng.Close() })
	if err := eng.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)
	m.AttachEngine(eng)
	m.reconstructInbox()
	return pump(t, m, m.loadRows), f
}

// A critique that asked for changes is not an approval prompt.
//
// The drive: the plan critique submitted VERDICT: changes with four
// blocking findings annotated in the spec, the run was stopped before
// the replan round it was going to feed, and the next `gummi` start met
// the card with "plan is ready for your decision" over a picker whose
// first row was "approve" — three lines under the verdict on the same
// screen saying the opposite. Live this state never reaches a reader at
// all (the loop bounces straight into the rework round), so every
// surface that draws the gate had been written for the pass.
func TestPlanGateAfterAChangesVerdictDoesNotAskForApproval(t *testing.T) {
	m, f := restartedAt(t, domain.StagePlan, state.SessionSnapshot{
		Role: "reviewer", Flavor: "critique", State: "done", Verdict: "changes",
	})

	it, ok := m.inbox.get(f.ID)
	if !ok {
		t.Fatal("the restored stop raised nothing")
	}
	if !strings.Contains(it.Text, "asked for changes") {
		t.Errorf("inbox line = %q, want it to name the changes verdict", it.Text)
	}
	// escalated, because the loop did not settle this — it is also what
	// makes the plan's own /bounce legal (msgs.go's bounceStage).
	if !it.Escalated {
		t.Errorf("the gate reads as a clean finish: %+v", it)
	}

	r := m.rows[0]
	in := m.nextInputFor(r)
	if in.verdict != verdictChanges || !critiqueUnsettled(in) {
		t.Fatalf("the verdict did not survive the restart: %v", in.verdict)
	}
	if got := whyItStopped(in); !strings.Contains(got, "asked for changes") {
		t.Errorf("narration = %q, want the verdict named", got)
	}
	d := m.openDecision(r)
	if d == nil || d.kind != decisionGate {
		t.Fatalf("decision = %+v, want a gate", d)
	}
	if strings.Contains(d.question, "ready for your decision") {
		t.Errorf("question = %q, want one that does not read as an invitation to approve", d.question)
	}

	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "run" {
		t.Fatalf("the picker leads with %+v, want the architect — acting on the findings is what moves this card", acts)
	}
	// approve is not withdrawn: a reader who reads the findings and
	// disagrees still crosses with g. It just no longer claims to be the
	// recommendation, and its own row says what it overrules.
	var approve *nextAction
	for i := range acts {
		if acts[i].id == "advance" {
			approve = &acts[i]
		}
	}
	if approve == nil {
		t.Fatalf("approve was dropped from the answer set: %+v", acts)
	}
	if approve.key != "g" {
		t.Errorf("approve moved off g: %+v", approve)
	}
	if !strings.Contains(approve.detail, "overrules") {
		t.Errorf("approve's row = %q, want it to say what crossing overrules", approve.detail)
	}
}

// The clean pass is untouched: approve leads, and says what it does.
func TestPlanGateAfterAPassStillLeadsWithApprove(t *testing.T) {
	m, _ := restartedAt(t, domain.StagePlan, state.SessionSnapshot{
		Role: "reviewer", Flavor: "critique", State: "done", Verdict: "pass",
	})
	in := m.nextInputFor(m.rows[0])
	if critiqueUnsettled(in) {
		t.Fatalf("a clean pass reads as unsettled: verdict=%v escalated=%v", in.verdict, in.escalated)
	}
	acts := stageActions(in)
	if len(acts) == 0 || acts[0].id != "advance" || acts[0].label != "approve" {
		t.Fatalf("the picker leads with %+v, want approve", acts)
	}
	if got := m.openDecision(m.rows[0]); got == nil || !strings.Contains(got.question, "ready for your decision") {
		t.Errorf("question = %+v, want the plain gate's", got)
	}
}

// The work stage's gate is the same gate one stop later: its picker used
// to lead with "start verify — the critique passed" whatever the
// critique had said.
func TestImplementGateAfterAChangesVerdictLeadsWithTheRework(t *testing.T) {
	m, _ := restartedAt(t, domain.StageImplement, state.SessionSnapshot{
		Role: "reviewer", Flavor: "critique", State: "done", Verdict: "changes",
	})
	in := m.nextInputFor(m.rows[0])
	if !critiqueUnsettled(in) {
		t.Fatalf("the verdict did not survive the restart: %v", in.verdict)
	}
	acts := stageActions(in)
	if len(acts) == 0 || !acts[0].sendBack {
		t.Fatalf("the picker leads with %+v, want the rework", acts)
	}
	for _, a := range acts {
		if strings.Contains(a.detail, "the critique passed") {
			t.Errorf("a row still claims the critique passed: %q", a.detail)
		}
	}
	if got := whyItStopped(in); strings.Contains(got, "critique passed") {
		t.Errorf("narration = %q, still claims the critique passed", got)
	}
}

// The same stop with no readable verdict at all: live that is the
// escalation arm (the loop gives up rather than passing it), and the
// escalation's own record does not always outlive the process — so the
// reconstruction has to reach the same conclusion from the session.
func TestPlanGateWithNoClearVerdictReconstructsAsAnEscalation(t *testing.T) {
	m, f := restartedAt(t, domain.StagePlan, state.SessionSnapshot{
		Role: "reviewer", Flavor: "critique", State: "done",
	})
	it, ok := m.inbox.get(f.ID)
	if !ok || !it.Escalated {
		t.Fatalf("reconstructed item = %+v, want an escalated gate", it)
	}
	if !strings.Contains(it.Text, "no clear verdict") {
		t.Errorf("inbox line = %q, want it to say the verdict was unreadable", it.Text)
	}
	in := m.nextInputFor(m.rows[0])
	if !critiqueUnsettled(in) {
		t.Fatalf("an unreadable verdict reads as settled: verdict=%v escalated=%v", in.verdict, in.escalated)
	}
	if acts := stageActions(in); len(acts) == 0 || acts[0].id != "run" {
		t.Fatalf("the picker leads with %+v, want the architect", acts)
	}
}
