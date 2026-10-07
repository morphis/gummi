package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// TestAFailedStageOffersRetryProfileAndStop: a stage whose run failed is
// not a paused run. Its decision offers a retry, another profile, and
// stopping there — and not "pick it back up — the run is paused", which
// was the only answer it used to have.
func TestAFailedStageOffersRetryProfileAndStop(t *testing.T) {
	in := nextInput{
		stage: domain.StageImplement, kind: domain.KindResearch, sess: engine.StatePaused,
		attn: attnFailure, attnText: "backend \"headless\" cannot enforce a read-only research session", profiles: true,
	}
	var ids []string
	for _, a := range stageActions(in) {
		ids = append(ids, a.id)
		if strings.Contains(a.why, "paused") || strings.Contains(a.label, "pick it back up") {
			t.Errorf("a failed stage is offered %q — %q", a.label, a.why)
		}
	}
	if strings.Join(ids, ",") != "run,profile,settle" {
		t.Fatalf("answers = %v, want try again, change profile, stop here", ids)
	}
	in.profiles = false
	ids = ids[:0]
	for _, a := range stageActions(in) {
		ids = append(ids, a.id)
	}
	if strings.Join(ids, ",") != "run,settle" {
		t.Errorf("answers with no profile to switch to = %v", ids)
	}
}

// TestAStageFailureIsRecordedOnceAndSurvivesARestart: the failure opens a
// decision, so a restart brings the card back waiting on it rather than
// idle; the question names the cause; and raising the same failure again
// with nothing run in between adds no second park or decision row.
func TestAStageFailureIsRecordedOnceAndSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	m := oneCardWorkspace(t)
	id := m.rows[0].F.ID
	const cause = "backend \"headless\" cannot enforce a read-only research session"

	m.raiseAttention(id, attnFailure, cause)
	m.inbox.remove(id)                       // a retry clears it …
	m.raiseAttention(id, attnFailure, cause) // … and fails the same way

	evs, err := m.store.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	parks, opens := 0, 0
	for _, ev := range evs {
		switch ev.Kind {
		case state.EventPark:
			parks++
		case state.EventDecisionOpen:
			opens++
		}
	}
	if parks != 1 || opens != 1 {
		t.Errorf("parks = %d, decisions = %d after the same failure twice; want one of each", parks, opens)
	}

	// a restart: a fresh board seeds its queue from the record
	open, err := m.store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.inbox.remove(id)
	m.seedInboxFromDecisions(open)
	it, ok := m.inbox.get(id)
	if !ok || it.Kind != attnFailure {
		t.Fatalf("after a restart the failure is forgotten: %+v", it)
	}
	in := m.nextInputFor(m.rows[0])
	if q := decisionQuestion(decisionFailure, m.rows[0], in); !strings.Contains(q, "cannot enforce") {
		t.Errorf("failure question = %q, want it to name the cause", q)
	}
}
