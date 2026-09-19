package driver

import (
	"context"
	"errors"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestACardWhoseDriveFailedLeavesNothingRunning: the conductor reads a
// card with a live session as RUNNING (`Engine.goalCardState`), so a
// session left behind by a drive that failed is a card the goal waits on
// for ever — no lead turn, no drop, no stall, no exit. A drive that failed
// leaves nothing working, and the session goes with it.
func TestACardWhoseDriveFailedLeavesNothingRunning(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Plan written.")
		},
	})
	ctx := context.Background()
	d := h.driver(Options{Autonomous: true})

	f, err := d.createFeature(ctx, domain.CardType{Kind: domain.KindFeature}, "a card of a goal")
	if err != nil {
		t.Fatal(err)
	}
	if f, err = h.store.Transition(ctx, f.ID, domain.StagePlan, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Attach(ctx, f); err != nil {
		t.Fatalf("starting the card's session: %v", err)
	}
	if h.eng.Get(f.ID) == nil {
		t.Fatal("no session to leave behind")
	}

	d.settleGoalCard(ctx, childResult{
		id: f.ID, out: Outcome{Status: StatusError, ID: string(f.ID)},
		err: errors.New("claude run failed: You've hit your session limit"),
	})

	if h.eng.Get(f.ID) != nil {
		t.Fatal("the card's session outlived the drive that failed; the goal will read it as still running and wait on it for ever")
	}
}

// TestACardsBackendOutageStallsTheGoalRatherThanCostingTheCard: a
// provider quota, a rate limit or an overload means "wait", and it means
// the same thing whether the turn was the lead's or a card's. Recorded as
// the card being stuck it costs two lead turns and then the card itself —
// work dropped for something no retry of the work could have fixed.
func TestACardsBackendOutageStallsTheGoalRatherThanCostingTheCard(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StagePlan: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Plan written.")
		},
	})
	ctx := context.Background()
	d := h.driver(Options{Autonomous: true})

	goal, err := d.createFeature(ctx, domain.CardType{Kind: domain.KindGoal}, "a goal")
	if err != nil {
		t.Fatal(err)
	}
	card, err := d.createFeature(ctx, domain.CardType{Kind: domain.KindFeature}, "a card of a goal")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetGoal(ctx, card.ID, goal.ID, false); err != nil {
		t.Fatal(err)
	}

	d.settleGoalCard(ctx, childResult{
		id: card.ID, out: Outcome{Status: StatusError, ID: string(card.ID)},
		err: errors.New("claude run failed: You've hit your session limit · resets 1:10am (UTC)"),
	})

	log, err := h.store.GoalLog(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	var outage bool
	for _, e := range log {
		if e.Outage {
			outage = true
			if e.Detail == "" {
				t.Errorf("the outage row carries none of the backend's own words")
			}
		}
	}
	if !outage {
		t.Fatalf("a card's drive failing on a provider quota was not recorded as the goal's outage: %+v", log)
	}
	for _, p := range parkRows(t, h, card.ID) {
		if p.Reason == state.ParkReasonGaveUp {
			t.Fatalf("the card was written off for the backend's outage: %q", p.Detail)
		}
	}
}
