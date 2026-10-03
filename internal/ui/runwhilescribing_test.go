package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// TestRunImplementWhileApprovalScribesRuns: approving a design gate
// starts its one-shot scribe passes (check discovery, the estimate), and
// a person who then asks for implement to run while those are still going
// must get a run — started, or queued behind them — never nothing.
func TestRunImplementWhileApprovalScribesRuns(t *testing.T) {
	rec := &architectRecorder{}
	m := cleanDesignGate(t, rec)

	msg := m.advanceStage("FD-001")()
	entered, ok := msg.(worktreeEnteredMsg)
	if !ok {
		t.Fatalf("approval returned %T (%+v), want worktreeEnteredMsg", msg, msg)
	}
	// the approval lands; its scribe passes are dispatched and left
	// running (their commands are not run here)
	model, _ := m.update(entered)
	m = model.(*Shell)
	if m.scribing["FD-001"] == 0 {
		t.Fatal("fixture: no scribe pass counted after the approval")
	}
	m = pump(t, m, m.loadRows)
	if st := m.rows[0].F.Stage; st != domain.StageImplement {
		t.Fatalf("fixture: approval left the card at %s", st)
	}

	d := m.openDecision(m.rows[0])
	if d == nil {
		t.Fatal("no decision at implement after the approval")
	}
	cursor := -1
	for i, a := range d.actions {
		if a.id == "run" {
			cursor = i
		}
	}
	if cursor < 0 {
		t.Fatalf("the decision offers no run: %+v", d.actions)
	}
	m = pump(t, m, m.answerDecisionAt(m.rows[0], d, cursor, nil))

	s := m.engine.Get("FD-001")
	if s == nil {
		t.Fatalf("run implement while the scribe passes ran started nothing (notice %q)", m.notice.text)
	}
	if st := s.State(); st != engine.StateRunning && st != engine.StateDone {
		t.Errorf("implement session state = %v", st)
	}
}
