package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// TestRewordOpenGateDecision pins what the reword touches: the newest
// still-open gate decision on the card, by OpenDecisions' own openness
// rules — leaving older open rows, answered rows and abandoned rows
// alone, and leaving the id a landing crossing correlates to intact.
func TestRewordOpenGateDecision(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	id, _ := domain.NewID(domain.KindResearch, 1)
	slug, _ := domain.Slugify("reword target")
	now := time.Now().UTC()
	f := &domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch,
		Title: "reword target", Slug: slug,
		Stage: domain.StageTodo, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, id, domain.StagePlan, "user"); err != nil {
		t.Fatal(err)
	}

	const oldID = "gate:RS-001:plan:1"
	const newID = "gate:RS-001:plan:2"
	const inviting = "plan critiqued: clean — review & approve"
	for _, p := range []DecisionPayload{
		{ID: oldID, Kind: DecisionKindGate, Question: inviting},
		{ID: newID, Kind: DecisionKindGate, Question: inviting},
	} {
		if err := s.OpenDecision(ctx, id, domain.StagePlan, p, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}

	// The newest open gate decision is the one reworded; the older open
	// one keeps its own wording.
	const blocker = "RS-001: 1 open question(s) block approval — resolve them"
	ok, err := s.RewordOpenGateDecision(ctx, id, blocker)
	if err != nil || !ok {
		t.Fatalf("RewordOpenGateDecision = %v, %v; want true, nil", ok, err)
	}
	opens, err := s.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opens[id]) != 2 {
		t.Fatalf("OpenDecisions reported %d rows for %s, want 2", len(opens[id]), id)
	}
	if opens[id][0].ID != oldID || opens[id][0].Question != inviting {
		t.Errorf("older decision = %q/%q, want %q with %q unchanged", opens[id][0].ID, opens[id][0].Question, oldID, inviting)
	}
	if opens[id][1].ID != newID || opens[id][1].Question != blocker {
		t.Errorf("newest decision = %q/%q, want %q reworded to %q", opens[id][1].ID, opens[id][1].Question, newID, blocker)
	}

	// Crossing the gate answers the reworded row — the id survived the
	// reword — and with no open gate decision left at the card's stage,
	// a second reword reports absent and touches nothing.
	if _, err := s.Transition(ctx, id, domain.StageImplement, "autopilot"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RewordOpenGateDecision(ctx, id, blocker); err != nil || ok {
		t.Fatalf("second RewordOpenGateDecision = %v, %v; want false, nil", ok, err)
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var answeredID string
	for _, ev := range events {
		if ev.Kind != EventGate {
			continue
		}
		var gp GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &gp); err != nil {
			t.Fatal(err)
		}
		if gp.From == string(domain.StagePlan) {
			answeredID = gp.ID
		}
	}
	if answeredID != newID {
		t.Errorf("the crossing out of %s correlates to %q, want the reworded decision %q", domain.StagePlan, answeredID, newID)
	}
}
