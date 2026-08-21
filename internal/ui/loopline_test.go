package ui

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

func loopShell() *Shell {
	return NewShell(theme.GummiDark(), "v0-test")
}

func planFeature() domain.Feature {
	return domain.Feature{ID: "FD-001", Stage: domain.StagePlan}
}

func TestPlanLoopLegQuietWithoutActivity(t *testing.T) {
	m := loopShell()
	if _, _, ok := m.planLoopLeg("FD-001"); ok {
		t.Error("leg reported with no session and no gate")
	}
	if line := m.planLoopLine(planFeature()); line != "" {
		t.Errorf("loop line rendered with nothing in flight: %q", line)
	}
}

func TestPlanLoopLegApproveOnGate(t *testing.T) {
	m := loopShell()
	m.inbox.add("FD-001", attnGate, "plan critiqued: clean — review & approve")
	leg, escalated, ok := m.planLoopLeg("FD-001")
	if !ok || leg != planLegApprove {
		t.Fatalf("gate did not map to the approve leg: leg=%d ok=%v", leg, ok)
	}
	if escalated {
		t.Error("clean gate reported as escalated")
	}
	line := m.planLoopLine(planFeature())
	if !strings.Contains(line, "approve") || !strings.Contains(line, "plan ✓") {
		t.Errorf("approve breadcrumb missing done/active legs: %q", line)
	}
}

func TestPlanLoopLegIgnoresNonGateAttention(t *testing.T) {
	m := loopShell()
	m.inbox.add("FD-001", attnFailure, "session errored")
	if _, _, ok := m.planLoopLeg("FD-001"); ok {
		t.Error("a failure item lit the approve leg")
	}
}

func TestPlanLoopLineOtherStageEmpty(t *testing.T) {
	m := loopShell()
	m.inbox.add("FD-001", attnGate, "review finished")
	f := domain.Feature{ID: "FD-001", Stage: domain.StageReview}
	if line := m.planLoopLine(f); line != "" {
		t.Errorf("loop line rendered off the plan stage: %q", line)
	}
}

func TestRunningLabelNamesPlanLegs(t *testing.T) {
	m := loopShell()
	f := planFeature()
	cases := []struct {
		name   string
		snap   engine.Snapshot
		rounds int
		want   string
	}{
		{"writer", engine.Snapshot{Feature: f}, 0, "writing plan"},
		{"critique", engine.Snapshot{Feature: f, Critique: true}, 0, "critiquing plan"},
		{"replan", engine.Snapshot{Feature: f}, 1, "replanning"},
		{"other stage", engine.Snapshot{Feature: domain.Feature{ID: "FD-001", Stage: domain.StageImplement}}, 0, "running"},
	}
	for _, c := range cases {
		m.setRound("FD-001", domain.RoundKindPlan, c.rounds)
		if got := m.runningLabel(c.snap); got != c.want {
			t.Errorf("%s: runningLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPlanLoopRoundCounterInBreadcrumb(t *testing.T) {
	m := loopShell()
	m.inbox.add("FD-001", attnGate, "escalated")
	m.setRound("FD-001", domain.RoundKindPlan, 1)
	line := m.planLoopLine(planFeature())
	if !strings.Contains(line, "replan") || !strings.Contains(line, "round 1/2") {
		t.Errorf("bounced loop breadcrumb missing replan/round: %q", line)
	}
}

func TestPlanLoopEscalatedGateTintsApprove(t *testing.T) {
	m := loopShell()
	m.inbox.addEscalated("FD-001", attnGate, "plan critique still requesting changes after 2 rounds — review the plan manually")
	_, escalated, ok := m.planLoopLeg("FD-001")
	if !ok || !escalated {
		t.Fatalf("escalated gate not reported: escalated=%v ok=%v", escalated, ok)
	}
	line := m.planLoopLine(planFeature())
	if !strings.Contains(line, "escalated") {
		t.Errorf("escalated breadcrumb missing the marker: %q", line)
	}
}

// End to end: a critique that never passes escalates, and the loop line
// marks the gate as escalated rather than finished-clean.
func TestPlanLoopLineAfterEscalation(t *testing.T) {
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "Still broken.\nVERDICT: changes"))

	line := m.planLoopLine(m.rows[0].F)
	if !strings.Contains(line, "approve") || !strings.Contains(line, "escalated") {
		t.Fatalf("capped loop did not render an escalated approve leg: %q", line)
	}
}

// End to end: after a clean critique run the dashboard's loop line shows
// the approval leg lit and both agent legs checked off.
func TestPlanLoopLineAfterCleanCritique(t *testing.T) {
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))

	line := m.planLoopLine(m.rows[0].F)
	if !strings.Contains(line, "approve") {
		t.Fatalf("clean critique did not light the approve leg: %q", line)
	}
	if !strings.Contains(line, "plan ✓") || !strings.Contains(line, "critique ✓") {
		t.Errorf("done legs not checked off: %q", line)
	}
}
