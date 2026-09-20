package state

// The observer's contract, pinned here: only hookable kinds, only
// committed rows (a deduped no-op is silent), payloads the log itself
// holds, and the two observer-only synthetics that are never rows.

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// observer collects what the store reports, in order.
type observer struct {
	events []CardEvent
}

func (o *observer) handle(ev CardEvent) { o.events = append(o.events, ev) }

func (o *observer) kinds() []string {
	out := make([]string, 0, len(o.events))
	for _, ev := range o.events {
		out = append(out, ev.Kind)
	}
	return out
}

func TestObserverTransitionReportsGate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, f.ID, domain.StagePlan, "user"); err != nil {
		t.Fatal(err)
	}

	if len(obs.events) != 2 {
		t.Fatalf("got %d observations (%v), want 2: created then gate", len(obs.events), obs.kinds())
	}
	if obs.events[0].Kind != EventCreated {
		t.Errorf("first event kind = %q, want %q", obs.events[0].Kind, EventCreated)
	}
	gate := obs.events[1]
	if gate.Kind != EventGate {
		t.Fatalf("second event kind = %q, want gate", gate.Kind)
	}
	var gp GatePayload
	if err := json.Unmarshal([]byte(gate.Payload), &gp); err != nil {
		t.Fatal(err)
	}
	if gp.From != string(domain.StageTodo) || gp.To != string(domain.StagePlan) || gp.Actor != "user" {
		t.Errorf("gate payload = %+v, want todo→plan by user", gp)
	}
}

func TestObserverVerifiedOnLanding(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(2, "CSV export")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		if _, err := s.Transition(ctx, f.ID, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetVerifiedAt(ctx, f.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	obs.events = nil
	if _, err := s.Transition(ctx, f.ID, domain.StageDone, "user"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(obs.kinds(), []string{EventGate, EventVerified}) {
		t.Fatalf("kinds = %v, want [gate verified]", obs.kinds())
	}
	v := obs.events[1]
	if v.Stage != domain.StageDone {
		t.Errorf("verified event stage = %q, want done", v.Stage)
	}
	if v.Payload != "" {
		t.Errorf("verified payload = %q, want empty (synthetic)", v.Payload)
	}
}

func TestObserverSilentWithoutVerifiedStamp(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(3, "No stamp")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.Stage{domain.StagePlan, domain.StageImplement, domain.StageVerify} {
		if _, err := s.Transition(ctx, f.ID, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	obs.events = nil
	if _, err := s.Transition(ctx, f.ID, domain.StageDone, "user"); err != nil {
		t.Fatal(err)
	}
	// A →done crossing without the stamp (the hand-off-like shape) is a
	// crossing, not a verified landing.
	if !slices.Equal(obs.kinds(), []string{EventGate}) {
		t.Fatalf("kinds = %v, want [gate] only", obs.kinds())
	}
}

func TestObserverDedupedInsertIsSilent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(4, "Once only")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	ev := CardEvent{Feature: f.ID, Stage: domain.StageTodo, Kind: EventPark, At: at, Dedupe: "one-park"}
	if err := s.AppendEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}
	parks := 0
	for _, got := range obs.events {
		if got.Kind == EventPark {
			parks++
		}
	}
	if parks != 1 {
		t.Errorf("park reported %d times, want 1 (the deduped no-op is silent)", parks)
	}
}

func TestObserverParkAndDecisionPayloads(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(5, "Waiting")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	obs.events = nil
	if err := s.AppendPark(ctx, f.ID, domain.StageVerify, ParkReasonNeedsYou, "verify failed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenDecision(ctx, f.ID, domain.StageVerify, DecisionPayload{
		ID: "d-1", Kind: DecisionKindGate, Question: "approve the plan?",
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(obs.kinds(), []string{EventPark, EventDecisionOpen}) {
		t.Fatalf("kinds = %v, want [park decision_open]", obs.kinds())
	}
	var pp ParkPayload
	if err := json.Unmarshal([]byte(obs.events[0].Payload), &pp); err != nil {
		t.Fatal(err)
	}
	if pp.Reason != ParkReasonNeedsYou || pp.Detail != "verify failed" {
		t.Errorf("park payload = %+v", pp)
	}
	var dp DecisionPayload
	if err := json.Unmarshal([]byte(obs.events[1].Payload), &dp); err != nil {
		t.Fatal(err)
	}
	if dp.ID != "d-1" || dp.Kind != DecisionKindGate || dp.Question != "approve the plan?" {
		t.Errorf("decision payload = %+v", dp)
	}
}

func TestObserverSkipsNarration(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(6, "Chatty")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	obs.events = nil
	at := time.Now().UTC()
	// The mirror batch's vocabulary: a stage run and its messages and
	// tool calls. None of it is a state change, so none of it reports.
	if err := s.AppendEvents(ctx, []CardEvent{
		{Feature: f.ID, Stage: domain.StageTodo, Kind: EventStageEnter, At: at},
		{Feature: f.ID, Stage: domain.StageTodo, Kind: EventMessage, At: at},
		{Feature: f.ID, Stage: domain.StageTodo, Kind: EventTool, At: at},
		{Feature: f.ID, Stage: domain.StageTodo, Kind: EventToolResult, At: at},
		{Feature: f.ID, Stage: domain.StageTodo, Kind: EventMessage, At: at},
	}); err != nil {
		t.Fatal(err)
	}
	if len(obs.events) != 0 {
		t.Errorf("narration reported: %v", obs.kinds())
	}
}

func TestObserverNilIsNoop(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(7, "Quiet")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, f.ID, domain.StagePlan, "user"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendPark(ctx, f.ID, domain.StagePlan, ParkReasonQuit, "board quit", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestObserverCloseGoalDroppedReports(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	obs := &observer{}
	s.SetObserver(obs.handle)

	f := feat(8, "Dropped")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.Stage{domain.StagePlan, domain.StageImplement} {
		if _, err := s.Transition(ctx, f.ID, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	obs.events = nil
	if err := s.CloseGoalDropped(ctx, f.ID, "auto", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(obs.kinds(), []string{EventGate}) {
		t.Fatalf("kinds = %v, want [gate] (a dropped card's stop, never a verified landing)", obs.kinds())
	}
	if got := obs.events[0].Stage; got != domain.StageImplement {
		t.Errorf("gate stage = %q, want the stage it was dropped from (implement)", got)
	}
}
